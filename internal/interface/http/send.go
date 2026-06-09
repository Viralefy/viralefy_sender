package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Viralefy/viralefy_sender/internal/application"
)

// sendResponse é o body devolvido por POST /internal/v1/send.
//
// status="queued" pra ambos os casos: novo enqueue OU idempotency hit.
// O caller (viralefy_api) não precisa distinguir — o ciclo do outbox vai
// despachar no próximo tick (ou já despachou se for retry rápido).
type sendResponse struct {
	Status    string `json:"status"`
	AttemptID string `json:"attempt_id"`
}

// SendHandler devolve o http.HandlerFunc de POST /internal/v1/send.
//
// Body esperado é application.SendRequest. Validação é delegada pro
// Service.Enqueue — handler só decoda + traduz erros pra HTTP.
func SendHandler(svc *application.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req application.SendRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":  "invalid_body",
				"detail": err.Error(),
			})
			return
		}
		attemptID, err := svc.Enqueue(r.Context(), req)
		if err != nil {
			// Validação retornada pela camada de aplicação: 400.
			// Não temos taxonomia de erros (Wave 3 pode introduzir),
			// então tudo cai em 400 com detail — exceto se for erro de
			// repo "outbox repo not configured" (boot mode), que vira 503.
			if errors.Is(err, errOutboxRepoMissing) || err.Error() == "outbox repo not configured" {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error":  "outbox_unavailable",
					"detail": err.Error(),
				})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":  "invalid_request",
				"detail": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, sendResponse{
			Status:    "queued",
			AttemptID: attemptID,
		})
	}
}

// errOutboxRepoMissing é um placeholder pro caso de comparar via errors.Is;
// como o Service hoje retorna errors.New direto, comparamos por string.
// Mantemos a var pra documentar a intenção quando subirmos pra sentinela.
var errOutboxRepoMissing = errors.New("outbox repo not configured")

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
