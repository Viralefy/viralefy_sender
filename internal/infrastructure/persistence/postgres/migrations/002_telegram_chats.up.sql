-- 002_telegram_chats.up.sql
-- Vínculo handle Telegram (@user) <-> chat_id numérico. O chat_id é capturado
-- quando o cliente manda /start pro bot (push do /internal/telegram/webhook).
-- Sem esse vínculo o bot não consegue enviar nada — Telegram não aceita envio
-- por handle, só por chat_id.
CREATE TABLE IF NOT EXISTS telegram_chats (
    handle     TEXT PRIMARY KEY,
    chat_id    BIGINT NOT NULL,
    linked_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Config do bot (1 linha — singleton conceitual, mas mantemos PK pra evolução).
-- bot_token_encrypted: AES-256-GCM(TWOFA_ENCRYPTION_KEY). admin_chat_id recebe
-- alertas internos (vendas, falhas, etc).
CREATE TABLE IF NOT EXISTS telegram_config (
    id                  TEXT PRIMARY KEY,
    bot_token_encrypted TEXT,
    admin_chat_id       BIGINT,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
