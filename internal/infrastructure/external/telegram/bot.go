// Package telegram implementa o cliente da Bot API do Telegram usado pelo
// viralefy_sender.
//
// Há dois fluxos:
//
//  1. Outbound (sender.outbox tick → SendMessage): após enqueue do template
//     telegram, o dispatcher resolve o handle (@user) em chat_id via
//     telegram_chats e chama sendMessage do Bot API (MarkdownV2 por padrão).
//
//  2. Inbound (Telegram → /internal/v1/telegram/webhook → HandleUpdate):
//     quando o cliente manda /start, capturamos from.id + from.username e
//     gravamos em telegram_chats. Sem isso o bot NÃO consegue enviar — o
//     Telegram API não aceita envio por handle, só por chat_id.
//
// Rate limit: Bot API permite ~30 mensagens/s globalmente. Dispatcher do
// outbox cuida do throttle no Wave 3 (TODO). Aqui só fazemos uma chamada
// HTTP simples — backoff em 429 fica no Service.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrHandleNotLinked é devolvido por ResolveHandle quando o usuário ainda
// não mandou /start pro bot — Telegram não permite envio sem chat_id, então
// o caller (Service.dispatch) deve falhar o envio com mensagem clara e não
// re-tentar indefinidamente.
var ErrHandleNotLinked = errors.New("telegram: handle not linked (user must /start the bot first)")

// ErrTokenMissing é devolvido por SendMessage quando o bot foi construído
// sem token (deploy sem TELEGRAM_BOT_TOKEN). Pra ser tratado pelo Service
// como "skip telegram, log + sent" — não queremos falhar venda por causa
// de notificação opcional.
var ErrTokenMissing = errors.New("telegram: bot token not configured")

// Bot é o cliente leve da Bot API.
//
// Reusa um único http.Client (default + timeout) e o mesmo pool pg pra
// resolver handles. Token vem cifrado em telegram_config no DB ou em claro
// via env (HML). Aqui o token já está decifrado em runtime — main do sender
// é o ponto de decryption (usa TwoFAEncryptionKey, ver config.Config).
type Bot struct {
	token      string
	httpClient *http.Client
	logger     *slog.Logger
	pool       *pgxpool.Pool
}

// NewBot devolve um *Bot pronto. Token vazio NÃO é erro — o sender pode
// rodar sem Telegram (LogSender-style). Caller (main) decide se instancia
// ou deixa nil; Service trata bot=nil como "skip telegram, mark sent".
func NewBot(token string, pool *pgxpool.Pool, logger *slog.Logger) *Bot {
	return &Bot{
		token:      strings.TrimSpace(token),
		pool:       pool,
		logger:     logger,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// HasToken indica se há token configurado. Service usa pra decidir se
// pula o channel telegram sem erro.
func (b *Bot) HasToken() bool { return b != nil && b.token != "" }

// sendMessageRequest é o payload do método sendMessage da Bot API.
type sendMessageRequest struct {
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// SendMessage despacha texto pro chat_id. parseMode vazio = texto plano;
// "MarkdownV2" requer que o caller já tenha escapado caracteres reservados
// do MV2 (\, *, _, etc). Sender mantém os templates Markdown V2 prontos
// no internal/application/templates/*_telegram.go.
func (b *Bot) SendMessage(ctx context.Context, chatID int64, text, parseMode string) error {
	if b == nil || b.token == "" {
		return ErrTokenMissing
	}
	payload, err := json.Marshal(sendMessageRequest{ChatID: chatID, Text: text, ParseMode: parseMode})
	if err != nil {
		return err
	}
	url := "https://api.telegram.org/bot" + b.token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram sendMessage: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ResolveHandle pega @username → chat_id da tabela telegram_chats. O handle
// é case-insensitive no Telegram, mas mantemos normalizado em lowercase no
// DB (UPSERT em HandleUpdate também faz lowercase). Aceita com ou sem '@'.
func (b *Bot) ResolveHandle(ctx context.Context, handle string) (int64, error) {
	if b == nil || b.pool == nil {
		return 0, ErrHandleNotLinked
	}
	h := normalizeHandle(handle)
	if h == "" {
		return 0, ErrHandleNotLinked
	}
	var chatID int64
	err := b.pool.QueryRow(ctx,
		`SELECT chat_id FROM telegram_chats WHERE handle = $1`, h,
	).Scan(&chatID)
	if err != nil {
		// Não diferenciamos sql.ErrNoRows aqui — qualquer falha de lookup é
		// tratada como "não linkado" pra que o Service decida (failed_final).
		return 0, ErrHandleNotLinked
	}
	return chatID, nil
}

// Update é o subset do Telegram Update payload que precisamos pra capturar
// /start. Bot API manda muito mais campos; ignoramos com json:"-" implicito.
type Update struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int `json:"message_id"`
		From      *struct {
			ID        int64  `json:"id"`
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
		} `json:"from"`
		Chat *struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		Text string `json:"text"`
		Date int64  `json:"date"`
	} `json:"message"`
}

// HandleUpdate processa um Update recebido via webhook. Se for /start
// (ou começa com /start), UPSERTa o vínculo @username → chat.id. Outros
// updates só são logados — o sender não responde proativamente (Wave 3
// pode adicionar comandos /pedidos, /suporte, conforme PHASE-8 §6).
func (b *Bot) HandleUpdate(ctx context.Context, u Update) error {
	if b == nil || u.Message == nil || u.Message.From == nil || u.Message.Chat == nil {
		return nil
	}
	text := strings.TrimSpace(u.Message.Text)
	if !strings.HasPrefix(text, "/start") {
		// Outros updates: log e ignora. Não bloqueamos o webhook pra que
		// o Telegram não fique reentregando — sempre devolvemos 200.
		if b.logger != nil {
			b.logger.Info("telegram update ignored",
				"update_id", u.UpdateID,
				"from_id", u.Message.From.ID,
				"text_len", len(text),
			)
		}
		return nil
	}
	username := strings.ToLower(strings.TrimSpace(u.Message.From.Username))
	if username == "" {
		// Sem @username público: ainda gravamos com sentinel "id:<chat_id>"
		// pra que admin possa enviar usando esse handle. PHASE-8 §6 trata
		// como follow-up — por ora, dropamos.
		if b.logger != nil {
			b.logger.Warn("telegram /start without username; skipping link",
				"chat_id", u.Message.Chat.ID,
			)
		}
		return nil
	}
	handle := "@" + username
	if b.pool == nil {
		if b.logger != nil {
			b.logger.Warn("telegram /start received but pg pool nil",
				"handle", handle, "chat_id", u.Message.Chat.ID,
			)
		}
		return nil
	}
	_, err := b.pool.Exec(ctx, `
		INSERT INTO telegram_chats (handle, chat_id, linked_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (handle) DO UPDATE SET chat_id = EXCLUDED.chat_id, linked_at = NOW()
	`, handle, u.Message.Chat.ID)
	if err != nil {
		return fmt.Errorf("upsert telegram_chats: %w", err)
	}
	if b.logger != nil {
		b.logger.Info("telegram handle linked",
			"handle", handle, "chat_id", u.Message.Chat.ID,
		)
	}
	return nil
}

// normalizeHandle aceita "@user", "user", "@USER" e devolve "@user"
// (lowercase). Vazio = erro upstream.
func normalizeHandle(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" {
		return ""
	}
	if !strings.HasPrefix(h, "@") {
		h = "@" + h
	}
	return h
}
