// Package config carrega configuração do viralefy_sender a partir de env vars.
//
// O microservice sobe na rede loopback da VPS (default 127.0.0.1:8082); só o
// viralefy_api é cliente legítimo. Defesa-em-profundidade via X-Internal-Token
// (mesmo segredo do payments), gerado pelo installer e persistido em
// /etc/viralefy/.env como INTERNAL_SHARED_SECRET.
//
// Convenções compartilhadas com viralefy_api:
//   - TWOFA_ENCRYPTION_KEY é reusada como key de cifragem AES-256 do bot token
//     do Telegram em rest (ver §8 do PHASE-8 — "mesma key, escopo diferente").
//   - DATABASE_URL aponta pro mesmo Postgres; este service só toca nas tabelas
//     sender_outbox, telegram_chats, telegram_config.
package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	// HTTP
	Port     string // SENDER_PORT (default 8082)
	BindHost string // SENDER_BIND_HOST (default 127.0.0.1 — loopback-only)

	// Persistência
	DatabaseURL string

	// Auth interno entre microservices
	InternalSharedSecret string

	// Email (Resend é o provider de produção atual)
	ResendAPIKey   string
	ResendFrom     string
	ResendFromName string
	ResendBaseURL  string

	// Email — provider override + SMTP fallback. EmailProvider="resend"
	// usa o ResendSender; senão SMTP (se Addr setado); senão LogSender.
	EmailProvider string
	SMTPAddr      string
	SMTPUser      string
	SMTPPass      string
	SMTPFrom      string
	SMTPFromName  string

	// Telegram (opcional — service sobe sem token; tick só dispara email).
	// Token é cifrado AES-256 com TwoFAEncryptionKey antes de persistir;
	// aqui carregamos o valor cifrado direto do env como fallback quando
	// telegram_config ainda não foi populado pelo backoffice.
	TelegramBotTokenEncrypted string

	// TelegramBotToken — versão em claro, prioridade sobre o cifrado.
	// Útil em HML/dev quando o operador não quer configurar AES key.
	// Em prod o fluxo padrão é cifrado em telegram_config (DB).
	TelegramBotToken string

	// TelegramWebhookSecret — secret_token configurado via setWebhook.
	// Telegram envia em todo request via X-Telegram-Bot-Api-Secret-Token.
	// Vazio = aceitamos sem validação (só pra dev local, jamais em prod).
	TelegramWebhookSecret string

	// AES-256 (32 bytes) — mesma key do 2FA da API, reusada pra cifrar o bot
	// token do Telegram em rest. Vazio = Telegram disabled.
	TwoFAEncryptionKey []byte
}

func Load() (Config, error) {
	cfg := Config{
		Port:                      getenv("SENDER_PORT", "8082"),
		BindHost:                  getenv("SENDER_BIND_HOST", "127.0.0.1"),
		DatabaseURL:               getenv("DATABASE_URL", "postgres://viralefy:viralefy@localhost:5432/viralefy?sslmode=disable"),
		InternalSharedSecret:      getenv("INTERNAL_SHARED_SECRET", ""),
		ResendAPIKey:              getenv("RESEND_API_KEY", ""),
		ResendFrom:                getenv("RESEND_FROM", "onboarding@resend.dev"),
		ResendFromName:            getenv("RESEND_FROM_NAME", "Viralefy"),
		ResendBaseURL:             getenv("RESEND_BASE_URL", "https://api.resend.com"),
		TelegramBotTokenEncrypted: getenv("TELEGRAM_BOT_TOKEN_ENCRYPTED", ""),
		TelegramBotToken:          getenv("TELEGRAM_BOT_TOKEN", ""),
		TelegramWebhookSecret:     getenv("TELEGRAM_WEBHOOK_SECRET", ""),
		EmailProvider:             getenv("EMAIL_PROVIDER", "resend"),
		SMTPAddr:                  getenv("SMTP_ADDR", ""),
		SMTPUser:                  getenv("SMTP_USER", ""),
		SMTPPass:                  getenv("SMTP_PASS", ""),
		SMTPFrom:                  getenv("SMTP_FROM", ""),
		SMTPFromName:              getenv("SMTP_FROM_NAME", "Viralefy"),
	}
	if raw := strings.TrimSpace(getenv("TWOFA_ENCRYPTION_KEY", "")); raw != "" {
		if b, err := parse2FAKey(raw); err == nil {
			cfg.TwoFAEncryptionKey = b
		}
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// parse2FAKey aceita hex 64 chars OU base64 44 (com padding) / 43 (sem).
// Mesma lógica do viralefy_api/internal/config — duplicado por design pra
// evitar dep cycle entre os módulos. Quando virar repo, fica idêntica.
func parse2FAKey(s string) ([]byte, error) {
	if len(s) == 64 {
		if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("TWOFA_ENCRYPTION_KEY must be 32 bytes (hex 64 or base64 44/43 chars)")
}
