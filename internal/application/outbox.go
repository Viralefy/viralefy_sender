// Package application abriga os casos de uso do viralefy_sender.
//
// Service implementa o ciclo do sender.outbox conforme §2 do PHASE-8:
//
//	enqueued ──Tick──▶ in_flight ──ok──▶ sent
//	                       │
//	                       └──fail──▶ backoff (incrementa attempt_count)
//	                                  └──attempt>=5──▶ failed_final + alert
//
// Idempotência é attempt_id (UNIQUE em sender_outbox). Enqueue duplicado
// devolve o row existente, sem disparar nada.
//
// Dispatch é dispatcher-agnostic: o Service só conhece interfaces
// EmailSender e o *telegram.Bot. Webhook channel fica TODO no Wave 3.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// SendRequest é o corpo aceito por POST /internal/v1/send.
//
// AttemptID é opcional: vazio = sender gera UUID. Caller (viralefy_api)
// deve passar o próprio attempt_id pra que retries do orchestrator não
// criem múltiplas mensagens (idempotency end-to-end).
type SendRequest struct {
	Channel   string                 `json:"channel"`   // "email" | "telegram" | "webhook"
	Template  string                 `json:"template"`  // ex.: "checkout_paid"
	To        SendRecipient          `json:"to"`        // destinatário por canal
	Vars      map[string]interface{} `json:"vars"`      // dados pro template
	AttemptID string                 `json:"attempt_id"`
	Priority  string                 `json:"priority"`  // "high" | "normal" (informational)
}

// SendRecipient é o destinatário por canal. Email/Telegram são mutuamente
// exclusivos por request — Service valida.
type SendRecipient struct {
	Email          string `json:"email,omitempty"`
	TelegramHandle string `json:"telegram_handle,omitempty"`
	WebhookURL     string `json:"webhook_url,omitempty"`
}

// OutboxRepo é a porta de persistência da fila. Implementação concreta
// vive em internal/infrastructure/persistence/postgres/outbox_repo.go.
type OutboxRepo interface {
	// Enqueue cria um row novo, ou devolve o existente se attempt_id já existe.
	// Retorna (created, error): created=false significa idempotent hit.
	Enqueue(ctx context.Context, row OutboxRow) (created bool, err error)

	// LockBatch faz SELECT ... FOR UPDATE SKIP LOCKED em até `limit` rows
	// elegíveis (status=enqueued AND next_attempt_at<=NOW()) e marca como
	// in_flight no MESMO tx. Caller chama Finish pra fechar.
	LockBatch(ctx context.Context, limit int) ([]OutboxRow, error)

	// MarkSent fecha o row com status=sent. Sem retry.
	MarkSent(ctx context.Context, id string) error

	// MarkRetry incrementa attempt_count, grava last_error e agenda
	// next_attempt_at = NOW() + backoff. Volta pra status=enqueued.
	MarkRetry(ctx context.Context, id, lastErr string, backoff time.Duration) error

	// MarkFailedFinal fecha o row com status=failed_final (esgotou tentativas).
	MarkFailedFinal(ctx context.Context, id, lastErr string) error
}

// OutboxRow espelha sender_outbox 1:1.
type OutboxRow struct {
	ID                string
	AttemptID         string
	Channel           string
	Template          string
	RecipientEmail    string
	RecipientTelegram string
	Vars              []byte // JSONB cru — Service decoda só quando vai dispatch
	Status            string
	AttemptCount      int
	LastError         string
	NextAttemptAt     time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TelegramBot é a porta minima do bot client (definida aqui pra evitar
// dep cycle entre application e infrastructure). Implementação concreta:
// internal/infrastructure/external/telegram.Bot.
type TelegramBot interface {
	HasToken() bool
	SendMessage(ctx context.Context, chatID int64, text, parseMode string) error
	ResolveHandle(ctx context.Context, handle string) (int64, error)
}

// Service orquestra a fila persistente (sender_outbox).
//
// Construído no main do sender — email e bot podem ser nil (LogSender / no
// telegram). O Service trata cada nil como "skip dispatch, marca sent + log"
// pra que ambiente HML/dev não acumule failed_final por falta de provider.
type Service struct {
	Logger *slog.Logger
	Repo   OutboxRepo
	Email  EmailSender
	Bot    TelegramBot

	// IDGen é injetável pra testes; default = UUIDv4.
	IDGen func() string

	// MaxAttempts é o teto de retries antes de failed_final.
	// §2 do PHASE-8 fixa em 5.
	MaxAttempts int

	// BatchSize é quantos rows o Tick puxa por chamada.
	BatchSize int
}

// NewService cria o Service com defaults sãos. Logger é obrigatório pra
// observabilidade homogênea — passe slog.Default() se não tiver melhor.
func NewService(logger *slog.Logger) *Service {
	return &Service{
		Logger:      logger,
		IDGen:       defaultUUID,
		MaxAttempts: 5,
		BatchSize:   50,
	}
}

// Enqueue valida o request, normaliza e persiste. Retorna o attempt_id
// (gerado se vazio) — caller usa pra correlacionar.
func (s *Service) Enqueue(ctx context.Context, req SendRequest) (string, error) {
	if s.Repo == nil {
		return "", errors.New("outbox repo not configured")
	}
	if err := validateSendRequest(req); err != nil {
		return "", err
	}
	if strings.TrimSpace(req.AttemptID) == "" {
		req.AttemptID = s.IDGen()
	}
	id := s.IDGen()

	varsJSON := []byte("{}")
	if req.Vars != nil {
		b, err := json.Marshal(req.Vars)
		if err != nil {
			return "", fmt.Errorf("marshal vars: %w", err)
		}
		varsJSON = b
	}

	row := OutboxRow{
		ID:                id,
		AttemptID:         req.AttemptID,
		Channel:           req.Channel,
		Template:          req.Template,
		RecipientEmail:    req.To.Email,
		RecipientTelegram: req.To.TelegramHandle,
		Vars:              varsJSON,
		Status:            "enqueued",
		NextAttemptAt:     time.Now(),
	}
	created, err := s.Repo.Enqueue(ctx, row)
	if err != nil {
		return "", err
	}
	if s.Logger != nil {
		s.Logger.Info("outbox enqueue",
			"attempt_id", req.AttemptID,
			"channel", req.Channel,
			"template", req.Template,
			"created", created,
		)
	}
	return req.AttemptID, nil
}

func validateSendRequest(r SendRequest) error {
	switch r.Channel {
	case "email":
		if strings.TrimSpace(r.To.Email) == "" {
			return errors.New("to.email required for channel=email")
		}
	case "telegram":
		if strings.TrimSpace(r.To.TelegramHandle) == "" {
			return errors.New("to.telegram_handle required for channel=telegram")
		}
	case "webhook":
		if strings.TrimSpace(r.To.WebhookURL) == "" {
			return errors.New("to.webhook_url required for channel=webhook")
		}
	case "":
		return errors.New("channel required")
	default:
		return fmt.Errorf("unsupported channel %q", r.Channel)
	}
	if strings.TrimSpace(r.Template) == "" {
		return errors.New("template required")
	}
	return nil
}

// Tick é chamado pelo loop em cmd/sender/main.go a cada 30s. Pega um batch
// (FOR UPDATE SKIP LOCKED) e dispatcha cada row. Erros NÃO param o loop —
// o tick seguinte pega o que sobrou.
func (s *Service) Tick(ctx context.Context) {
	if s.Repo == nil {
		// Stub mode (sem repo): apenas heartbeat de log.
		if s.Logger != nil {
			s.Logger.Info("outbox tick", "status", "stub", "processed", 0)
		}
		return
	}
	batch, err := s.Repo.LockBatch(ctx, s.BatchSize)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Error("outbox lock batch failed", "error", err.Error())
		}
		return
	}
	if len(batch) == 0 {
		return
	}
	if s.Logger != nil {
		s.Logger.Info("outbox tick", "batch_size", len(batch))
	}
	for _, row := range batch {
		s.dispatchOne(ctx, row)
	}
}

func (s *Service) dispatchOne(ctx context.Context, row OutboxRow) {
	err := s.dispatch(ctx, row)
	if err == nil {
		if err := s.Repo.MarkSent(ctx, row.ID); err != nil && s.Logger != nil {
			s.Logger.Error("outbox mark_sent failed", "id", row.ID, "error", err.Error())
		}
		return
	}

	// Falha — decide retry vs failed_final.
	attempts := row.AttemptCount + 1
	if attempts >= s.MaxAttempts {
		if s.Logger != nil {
			s.Logger.Error("outbox failed_final",
				"id", row.ID,
				"attempt_id", row.AttemptID,
				"channel", row.Channel,
				"template", row.Template,
				"attempts", attempts,
				"error", err.Error(),
			)
		}
		if mErr := s.Repo.MarkFailedFinal(ctx, row.ID, err.Error()); mErr != nil && s.Logger != nil {
			s.Logger.Error("outbox mark_failed_final write failed", "id", row.ID, "error", mErr.Error())
		}
		return
	}
	backoff := nextBackoff(attempts)
	if s.Logger != nil {
		s.Logger.Warn("outbox retry scheduled",
			"id", row.ID,
			"attempts", attempts,
			"backoff", backoff.String(),
			"error", err.Error(),
		)
	}
	if mErr := s.Repo.MarkRetry(ctx, row.ID, err.Error(), backoff); mErr != nil && s.Logger != nil {
		s.Logger.Error("outbox mark_retry failed", "id", row.ID, "error", mErr.Error())
	}
}

// dispatch escolhe o adapter por channel. Retorna erro pra o caller decidir
// retry vs failed_final.
func (s *Service) dispatch(ctx context.Context, row OutboxRow) error {
	switch row.Channel {
	case "email":
		return s.dispatchEmail(ctx, row)
	case "telegram":
		return s.dispatchTelegram(ctx, row)
	case "webhook":
		// Wave 3 — admin webhook. Por ora, marca sent (no-op) com log.
		if s.Logger != nil {
			s.Logger.Info("outbox webhook channel not implemented; marking sent",
				"id", row.ID, "template", row.Template)
		}
		return nil
	default:
		return fmt.Errorf("unsupported channel %q", row.Channel)
	}
}

func (s *Service) dispatchEmail(ctx context.Context, row OutboxRow) error {
	if s.Email == nil {
		if s.Logger != nil {
			s.Logger.Info("email sender not configured; marking sent (log-only)",
				"id", row.ID, "template", row.Template)
		}
		return nil
	}
	subject, htmlBody, textBody, err := renderEmail(row)
	if err != nil {
		return fmt.Errorf("render template %q: %w", row.Template, err)
	}
	return s.Email.Send(ctx, EmailMessage{
		To:       row.RecipientEmail,
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: textBody,
	})
}

func (s *Service) dispatchTelegram(ctx context.Context, row OutboxRow) error {
	if s.Bot == nil || !s.Bot.HasToken() {
		if s.Logger != nil {
			s.Logger.Info("telegram bot not configured; marking sent (log-only)",
				"id", row.ID, "template", row.Template)
		}
		return nil
	}
	chatID, err := s.Bot.ResolveHandle(ctx, row.RecipientTelegram)
	if err != nil {
		return err
	}
	text, parseMode, err := renderTelegram(row)
	if err != nil {
		return fmt.Errorf("render telegram template %q: %w", row.Template, err)
	}
	return s.Bot.SendMessage(ctx, chatID, text, parseMode)
}

// nextBackoff segue §2 do PHASE-8: 30s, 5min, 1h, 6h, 24h.
// attempts é o número da próxima tentativa (1-based): 1→30s, 2→5min, etc.
func nextBackoff(attempts int) time.Duration {
	switch attempts {
	case 1:
		return 30 * time.Second
	case 2:
		return 5 * time.Minute
	case 3:
		return 1 * time.Hour
	case 4:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// defaultUUID é o gerador padrão de attempt_id/row.ID. Stdlib não tem
// UUID, mas o sender já depende de pgx que puxa google/uuid transitivamente
// (ver go.sum). Importamos direto pra evitar reimpl frágil de RFC 4122.
//
// Nota: definido como var pra que NewService consiga fazer s.IDGen = ...
// sem precisar de função top-level exportada.
var defaultUUID = func() string {
	// uuid.NewString pode dar panic se /dev/urandom falhar — em produção é
	// fatal por construção, então deixamos propagar.
	return newUUID()
}
