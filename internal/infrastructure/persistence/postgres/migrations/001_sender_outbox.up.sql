-- 001_sender_outbox.up.sql
-- Fila persistente de mensagens a despachar (email/telegram/webhook).
-- O service roda um tick a cada 30s que pega rows com status='enqueued' e
-- next_attempt_at <= NOW(), marca 'in_flight', tenta entregar e atualiza pra
-- 'sent' ou 'failed_final'. attempt_id é a chave de idempotência fornecida
-- pelo viralefy_api — segundo envio com mesmo attempt_id é no-op.
CREATE TABLE IF NOT EXISTS sender_outbox (
    id                  TEXT PRIMARY KEY,
    attempt_id          TEXT NOT NULL UNIQUE,
    channel             TEXT NOT NULL CHECK (channel IN ('email', 'telegram', 'webhook')),
    template            TEXT NOT NULL,
    recipient_email     TEXT,
    recipient_telegram  TEXT,
    vars                JSONB NOT NULL DEFAULT '{}'::jsonb,
    status              TEXT NOT NULL DEFAULT 'enqueued'
                              CHECK (status IN ('enqueued', 'in_flight', 'sent', 'failed_final')),
    attempt_count       INT  NOT NULL DEFAULT 0,
    last_error          TEXT,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Índice pro batch do tick: pega só o que está pronto pra tentar.
CREATE INDEX IF NOT EXISTS sender_outbox_ready_idx
    ON sender_outbox (next_attempt_at)
    WHERE status = 'enqueued';
