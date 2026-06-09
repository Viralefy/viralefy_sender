package http

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/Viralefy/viralefy_sender/internal/application"
	"github.com/Viralefy/viralefy_sender/internal/infrastructure/external/telegram"
)

// Deps agrupa as dependências do router. Estabilizamos a forma agora pra
// que os handlers tenham acesso a Service+Bot sem múltiplos argumentos
// posicionais — adicionar Webhook channel (Wave 3) só precisa de campo
// novo aqui.
type Deps struct {
	InternalSecret       string
	Service              *application.Service
	Bot                  *telegram.Bot
	TelegramWebhookSecret string
	Logger               *slog.Logger
}

// NewRouter monta o handler raiz do viralefy_sender.
//
// Rotas:
//
//	GET  /internal/health                         (sem auth, pra probes)
//	POST /internal/v1/send                        (X-Internal-Token)
//	POST /internal/v1/telegram/webhook            (X-Telegram-Bot-Api-Secret-Token)
//
// O webhook do Telegram fica FORA do middleware InternalAuth porque o
// Telegram só consegue mandar o header próprio dele. Auth desse path é
// feita inline pelo TelegramWebhookHandler (constant-time compare).
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	// OTel HTTP middleware: cria span por request + propaga W3C trace context.
	r.Use(func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "viralefy-sender",
			otelhttp.WithSpanNameFormatter(func(_ string, req *http.Request) string {
				return req.Method + " " + req.URL.Path
			}),
		)
	})

	r.Get("/internal/health", health)

	// /internal/v1/send protegido por X-Internal-Token.
	r.Group(func(g chi.Router) {
		g.Use(InternalAuth(d.InternalSecret))
		if d.Service != nil {
			g.Post("/internal/v1/send", SendHandler(d.Service))
		} else {
			g.Post("/internal/v1/send", sendStub)
		}
	})

	// /internal/v1/telegram/webhook — auth via header do Telegram, fora do
	// middleware InternalAuth. Quando bot=nil o handler devolve 503.
	r.Post("/internal/v1/telegram/webhook", TelegramWebhookHandler(d.Bot, d.TelegramWebhookSecret, d.Logger))

	return r
}

// health responde 200 com {"status":"ok"} — idempotente, sem tocar DB.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// sendStub é o fallback se o Service não foi injetado — modo boot mínimo.
// Em prod sempre temos Service; o stub fica pra dev sem DB.
func sendStub(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":  "not_implemented",
		"detail": "sender outbox not wired (Service nil)",
	})
}
