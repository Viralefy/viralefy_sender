// Command sender é o entrypoint do microservice viralefy_sender.
//
// Sobe na rede loopback (default 127.0.0.1:8082), expõe:
//
//	GET  /internal/health
//	POST /internal/v1/send                 (X-Internal-Token)
//	POST /internal/v1/telegram/webhook     (X-Telegram-Bot-Api-Secret-Token)
//
// O tick do outbox roda a cada 30s (configurável via outboxTickInterval),
// puxa batches de até 50 rows com FOR UPDATE SKIP LOCKED, dispatcha por
// canal e aplica backoff exponencial (30s, 5m, 1h, 6h, 24h) até 5 tentativas
// — conforme §2 do PHASE-8.
//
// Sentry/OTel/slog seguem o mesmo padrão do viralefy_api pra log/trace ficar
// homogêneo no Loki/Tempo (tag service=viralefy-sender). Quando SENTRY_DSN
// está vazio, o sentry-go vira no-op transparente.
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/Viralefy/viralefy_sender/internal/application"
	"github.com/Viralefy/viralefy_sender/internal/config"
	"github.com/Viralefy/viralefy_sender/internal/infrastructure/external/email"
	"github.com/Viralefy/viralefy_sender/internal/infrastructure/external/telegram"
	"github.com/Viralefy/viralefy_sender/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_sender/internal/infrastructure/persistence/postgres"
	httphandler "github.com/Viralefy/viralefy_sender/internal/interface/http"
)

// outboxTickInterval — alinhado ao §2 do PHASE-8 (retry tick a cada 30s).
const outboxTickInterval = 30 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	version := os.Getenv("APP_VERSION")
	if version == "" {
		version = "dev"
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).With(
		"service", "viralefy-sender",
		"version", version,
	)
	slog.SetDefault(logger)

	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              os.Getenv("SENTRY_DSN"),
		Release:          "viralefy-sender@" + version,
		Environment:      os.Getenv("APP_ENV"),
		AttachStacktrace: true,
	}); err != nil {
		logger.Warn("sentry init failed; continuing without it", "error", err.Error())
	}
	defer sentry.Flush(2 * time.Second)

	// Prometheus collectors — registrados antes de servir requests pra
	// /internal/metrics não 404ar enquanto handlers ainda warming up.
	observability.InitMetrics()

	// Postgres — boot falha rápido se DB não responde. Migrations isoladas
	// no schema do sender (ver §4 do PHASE-8).
	ctx := context.Background()
	db, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connect failed", "error", err.Error())
		log.Fatal("database:", err)
	}
	defer db.Close()

	if err := postgres.RunMigrations(ctx, db); err != nil {
		log.Fatal("migrate:", err)
	}

	// Email — Resend em prod, SMTP fallback, LogSender em dev.
	emailSender := email.New(email.Config{
		Provider:       cfg.EmailProvider,
		Addr:           cfg.SMTPAddr,
		User:           cfg.SMTPUser,
		Pass:           cfg.SMTPPass,
		From:           cfg.SMTPFrom,
		FromName:       cfg.SMTPFromName,
		ResendAPIKey:   cfg.ResendAPIKey,
		ResendFrom:     cfg.ResendFrom,
		ResendFromName: cfg.ResendFromName,
		ResendBaseURL:  cfg.ResendBaseURL,
	}, logger)

	// Telegram bot — opcional. Sem token => bot=nil e Service trata como
	// log-only no canal telegram. Suporte ao token cifrado (AES com
	// TwoFAEncryptionKey + telegram_config no DB) fica pra Wave 3, quando
	// o backoffice ganhar a tela de config; por ora carregamos TELEGRAM_BOT_TOKEN
	// em claro do env (HML/dev).
	var bot *telegram.Bot
	if tok := strings.TrimSpace(cfg.TelegramBotToken); tok != "" {
		bot = telegram.NewBot(tok, db.Pool(), logger)
		logger.Info("telegram bot enabled")
	} else {
		logger.Info("telegram bot disabled (TELEGRAM_BOT_TOKEN empty)")
	}

	// Outbox Service — recebe repo + email + bot. Tick agora roda real.
	outboxSvc := application.NewService(logger)
	outboxSvc.Repo = postgres.NewOutboxRepo(db.Pool())
	outboxSvc.Email = emailSender
	if bot != nil {
		outboxSvc.Bot = bot
	}

	tickCtx, tickCancel := context.WithCancel(context.Background())
	defer tickCancel()
	go runOutboxTick(tickCtx, outboxSvc, logger)

	// HTTP router — health + /internal/v1/send + /internal/v1/telegram/webhook.
	router := httphandler.NewRouter(httphandler.Deps{
		InternalSecret:        cfg.InternalSharedSecret,
		Service:               outboxSvc,
		Bot:                   bot,
		TelegramWebhookSecret: cfg.TelegramWebhookSecret,
		Logger:                logger,
	})
	addr := cfg.BindHost + ":" + cfg.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("viralefy_sender listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("listen failed", "error", err.Error())
			log.Fatal(err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// runOutboxTick dispara Tick a cada outboxTickInterval até o ctx cancelar.
// Erros do Tick são tratados internamente (loga + segue) — este loop só
// agenda. Primeira execução é imediata pra acelerar feedback no boot.
func runOutboxTick(ctx context.Context, svc *application.Service, logger *slog.Logger) {
	logger.Info("outbox tick scheduled", "interval", outboxTickInterval.String())
	t := time.NewTicker(outboxTickInterval)
	defer t.Stop()
	svc.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			svc.Tick(ctx)
		}
	}
}
