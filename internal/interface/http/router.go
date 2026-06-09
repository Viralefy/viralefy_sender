package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewRouter monta o handler raiz do viralefy_sender. Tudo abaixo de
// /internal/* exige X-Internal-Token; /internal/health é exceção pra
// que o systemd/Caddy possa fazer probes sem precisar do segredo.
func NewRouter(internalSecret string) http.Handler {
	r := chi.NewRouter()

	// OTel HTTP middleware: cria span por request + propaga W3C trace context.
	// Mantém paridade com viralefy_api (mesma stack de tracing).
	r.Use(func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "viralefy-sender",
			otelhttp.WithSpanNameFormatter(func(_ string, req *http.Request) string {
				return req.Method + " " + req.URL.Path
			}),
		)
	})

	r.Get("/internal/health", health)

	// Mount /internal/send atrás do middleware de auth interno. Quando o
	// handler real chegar (Wave 2) ele só precisa ser plugado aqui — auth +
	// tracing já estão prontos.
	r.Group(func(g chi.Router) {
		g.Use(InternalAuth(internalSecret))
		g.Post("/internal/send", sendStub)
	})

	return r
}

// health responde 200 com {"status":"ok"} — idempotente, sem tocar DB.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// sendStub é o placeholder do /internal/send até o Wave 2 escrever o
// handler real (validar payload, persistir em sender_outbox, devolver
// attempt_id). Por ora devolve 501 — viralefy_api detecta e cai no fluxo
// in-memory legacy.
func sendStub(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":  "not_implemented",
		"detail": "sender outbox not wired yet — see PHASE-8 Wave 2",
	})
}
