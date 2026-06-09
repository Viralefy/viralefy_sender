// Command sender é o entrypoint do microservice viralefy_sender.
//
// Sobe na rede loopback (default 127.0.0.1:8082), expõe /internal/health
// + /internal/send (stub 501 por ora), e roda um tick do outbox a cada 30s
// pra drenar a fila persistente sender_outbox. Migrations são aplicadas no
// boot — service é dono das tabelas sender_*, telegram_*.
//
// Sentry/OTel/slog seguem o mesmo padrão do viralefy_api pra log/trace ficar
// homogêneo no Loki/Tempo (tag service=viralefy-sender). Quando SENTRY_DSN
// está vazio, o sentry-go vira no-op transparente — não precisa de guard.
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/Viralefy/viralefy_sender/internal/application"
	"github.com/Viralefy/viralefy_sender/internal/config"
	"github.com/Viralefy/viralefy_sender/internal/infrastructure/persistence/postgres"
	httphandler "github.com/Viralefy/viralefy_sender/internal/interface/http"
)

// outboxTickInterval — alinhado ao §2 do PHASE-8 (retry tick a cada 30s).
// Mantemos como const pra ficar grep-able quando o Wave 2 tunar o valor.
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

	// Sentry — no-op quando SENTRY_DSN vazio. Flush no shutdown gracioso.
	// Mantemos init mesmo em dev pra que a stack trace chegue ao Sentry se
	// o operador apontar o DSN sem rebuild.
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              os.Getenv("SENTRY_DSN"),
		Release:          "viralefy-sender@" + version,
		Environment:      os.Getenv("APP_ENV"),
		AttachStacktrace: true,
	}); err != nil {
		logger.Warn("sentry init failed; continuing without it", "error", err.Error())
	}
	defer sentry.Flush(2 * time.Second)

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

	// Outbox tick — placeholder. No Wave 2 ganha repo + dispatcher real
	// (email via Resend, telegram via Bot API, backoff exponencial).
	outboxSvc := application.NewService(logger)
	tickCtx, tickCancel := context.WithCancel(context.Background())
	defer tickCancel()
	go runOutboxTick(tickCtx, outboxSvc, logger)

	// HTTP router — /internal/health (livre) + /internal/send (501 stub
	// atrás do InternalAuth). OTel middleware embutido no NewRouter.
	router := httphandler.NewRouter(cfg.InternalSharedSecret)
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
// Erros do Tick (quando houver lógica real) precisam ser tratados internamente
// — esta loop só agenda. Primeira execução é imediata pra acelerar feedback
// no boot, depois respeita o ticker.
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
