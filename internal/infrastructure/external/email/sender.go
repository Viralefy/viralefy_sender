// Package email é o adapter (ACL) de saída para envio de e-mail.
//
// Migrado do viralefy_api/internal/infrastructure/external/email (PHASE-8 §2).
// Implementa application.EmailSender com três backends:
//   - ResendSender   — produção, fala HTTP com api.resend.com
//   - SMTPSender     — fallback genérico via net/smtp + STARTTLS
//   - LogSender      — dev / sender sem provider configurado; só loga
//
// A seleção fica no main do sender (cmd/sender/main.go) — `New(Config)` aqui
// só centraliza a heurística pra manter paridade com o monolito.
package email

import (
	"context"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"

	"github.com/Viralefy/viralefy_sender/internal/application"
)

// Config descreve o provider de email. Provider="resend" + ResendAPIKey
// dispara o ResendSender; senão SMTP (se Addr setado); senão LogSender.
type Config struct {
	Provider string // "resend" usa a API do Resend; senão SMTP.

	// SMTP
	Addr     string // host:port (ex.: smtp.gmail.com:587). Vazio = log-only.
	User     string
	Pass     string
	From     string
	FromName string

	// Resend
	ResendAPIKey   string
	ResendFrom     string
	ResendFromName string
	ResendBaseURL  string // default https://api.resend.com (configurável p/ testes)
}

// New escolhe o EmailSender: Resend (se EMAIL_PROVIDER=resend e há API key),
// senão SMTP (se há Addr), senão LogSender (dev). Logger é opcional — quando
// nil, LogSender vira no-op silencioso.
func New(cfg Config, logger *slog.Logger) application.EmailSender {
	if cfg.Provider == "resend" && strings.TrimSpace(cfg.ResendAPIKey) != "" {
		base := cfg.ResendBaseURL
		if base == "" {
			base = "https://api.resend.com"
		}
		from := cfg.ResendFrom
		if from == "" {
			from = "onboarding@resend.dev"
		}
		if logger != nil {
			logger.Info("email provider selected", "provider", "resend", "from", from)
		}
		return &ResendSender{apiKey: cfg.ResendAPIKey, from: from, fromName: cfg.ResendFromName, baseURL: base}
	}
	if strings.TrimSpace(cfg.Addr) == "" {
		if logger != nil {
			logger.Warn("email provider not configured; using LogSender (dev only)")
		}
		return &LogSender{Logger: logger}
	}
	if cfg.From == "" {
		cfg.From = "no-reply@viralefy.local"
	}
	if logger != nil {
		logger.Info("email provider selected", "provider", "smtp", "addr", cfg.Addr)
	}
	return &SMTPSender{cfg: cfg}
}

// SMTPSender envia via net/smtp.SendMail, que faz STARTTLS automaticamente
// quando o servidor anuncia (porta 587) e autentica quando há credenciais.
type SMTPSender struct{ cfg Config }

func (s *SMTPSender) Send(_ context.Context, msg application.EmailMessage) error {
	host := s.cfg.Addr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	var auth smtp.Auth
	if s.cfg.User != "" {
		auth = smtp.PlainAuth("", s.cfg.User, s.cfg.Pass, host)
	}
	from := s.cfg.From
	fromHeader := from
	if s.cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", s.cfg.FromName, from)
	}
	body := msg.TextBody
	if body == "" {
		body = msg.HTMLBody
	}
	raw := buildMessage(fromHeader, msg.To, msg.Subject, body)
	return smtp.SendMail(s.cfg.Addr, auth, from, []string{msg.To}, raw)
}

func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

// LogSender apenas registra que um e-mail seria enviado, sem expor o corpo
// (que pode conter password de autocadastro). Usado quando não há provider
// configurado — fluxo dev/HML.
type LogSender struct {
	Logger *slog.Logger
}

func (l *LogSender) Send(_ context.Context, msg application.EmailMessage) error {
	if l.Logger == nil {
		return nil
	}
	// PII: mascaramos o destinatário pra não vazar email em log estruturado.
	l.Logger.Info("email (log-only)",
		"to_masked", maskEmail(msg.To),
		"subject", msg.Subject,
	)
	return nil
}

// maskEmail: u***@example.com — preserva domínio para debug, oculta local part.
// Cópia local do helper do viralefy_api/internal/infrastructure/observability
// pra evitar dep cycle entre os módulos.
func maskEmail(e string) string {
	at := strings.IndexByte(e, '@')
	if at <= 0 {
		return "***"
	}
	local, domain := e[:at], e[at:]
	if len(local) == 0 {
		return "***" + domain
	}
	return string(local[0]) + "***" + domain
}
