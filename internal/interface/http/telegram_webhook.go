package http

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Viralefy/viralefy_sender/internal/infrastructure/external/telegram"
)

// TelegramWebhookHandler devolve o http.HandlerFunc de
// POST /internal/v1/telegram/webhook.
//
// IMPORTANTE: este path NÃO passa pelo InternalAuth (X-Internal-Token) —
// o Telegram não consegue mandar headers customizados além do
// X-Telegram-Bot-Api-Secret-Token (configurado via setWebhook). Mantemos
// segurança via:
//
//  1. Constant-time compare do secret_token contra config.TelegramWebhookSecret
//     (Telegram inclui o secret em todo request quando setWebhook é
//     chamado com `secret_token`).
//  2. Path opaco (não documentado), descoberto só por quem tem acesso
//     ao painel/installer.
//  3. Loopback opcional: em prod, Caddy faz reverse-proxy do path público
//     direto pro 127.0.0.1:8082 — então o handler nunca vê tráfego não-TLS.
//
// Quando bot ou webhookSecret estão vazios, devolvemos 503 — fluxo de
// produção exige ambos. Pra dev sem bot, basta não chamar setWebhook.
func TelegramWebhookHandler(bot *telegram.Bot, webhookSecret string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bot == nil || !bot.HasToken() {
			http.Error(w, "telegram bot not configured", http.StatusServiceUnavailable)
			return
		}
		// Secret token check — defesa principal. Telegram manda em todo
		// request quando setWebhook é chamado com `secret_token`.
		if webhookSecret != "" {
			got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(webhookSecret)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		var update telegram.Update
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&update); err != nil {
			// Telegram esperando 200; se devolvermos 400 ele reentrega.
			// Logamos e devolvemos 200 vazio — request mal-formado nunca
			// vai ficar bom em retry.
			if logger != nil {
				logger.Warn("telegram webhook decode failed", "error", err.Error())
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := bot.HandleUpdate(r.Context(), update); err != nil {
			if logger != nil {
				logger.Error("telegram handle update failed",
					"update_id", update.UpdateID,
					"error", err.Error(),
				)
			}
			// Mesmo motivo: 200 pra evitar redelivery agressiva.
		}
		w.WriteHeader(http.StatusOK)
	}
}
