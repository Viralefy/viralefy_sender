# viralefy_sender

Microsserviço Go responsável por toda entrega de mensagem ao cliente da Viralefy
(email, Telegram bot, futuro WhatsApp / SMS / push). Extraído do monólito
`viralefy_api` na Fase 8 — ver
[PHASE-8-MICROSERVICES.md](../viralefy_archive/PHASE-8-MICROSERVICES.md) §2
para o plano de carve-out completo.

> Status: **scaffolding** (Wave 1). O `/internal/send` ainda devolve `501 Not
> Implemented`; o outbox tick é placeholder. Wave 2 move o código de email do
> monólito e implementa o dispatcher.

## Porta e exposição

- Escuta em **`127.0.0.1:8082`** por default (`SENDER_BIND_HOST` / `SENDER_PORT`).
- **Loopback-only**: Caddy não publica este service. O único cliente legítimo
  é o `viralefy_api`. Token interno (`X-Internal-Token`) é defesa-em-profundidade
  contra um eventual misconfig do Caddy/UFW.

## Fluxo do outbox

```
viralefy_api  --POST /internal/send-->  sender_outbox (status=enqueued)
                                                |
                                                | tick a cada 30s
                                                v
                                       application.Service.Tick
                                                |
                       +------------------------+------------------------+
                       v                        v                        v
                email (Resend)        telegram (Bot API)        webhook (admin)
                       |                        |                        |
                       +------------------------+------------------------+
                                                |
                          sucesso -> status=sent / falha -> backoff
                          attempt_count++ ; next_attempt_at += 30s/5m/1h/6h/24h
                          attempt_count >= 5 -> status=failed_final (alerta)
```

Idempotência: `attempt_id` (uuid) vem do `viralefy_api` e é `UNIQUE` na tabela
`sender_outbox`. Re-envio com mesmo `attempt_id` é no-op.

## Endpoints

| Método | Path                          | Auth              | Estado   |
|--------|-------------------------------|-------------------|----------|
| GET    | `/internal/health`            | livre             | OK       |
| POST   | `/internal/send`              | `X-Internal-Token`| 501 stub |
| POST   | `/internal/telegram/webhook`  | (futuro)          | TBD      |

## Variáveis de ambiente

| Var                            | Default                     | Descrição |
|--------------------------------|-----------------------------|-----------|
| `SENDER_PORT`                  | `8082`                      | Porta HTTP. |
| `SENDER_BIND_HOST`             | `127.0.0.1`                 | Default loopback. Mudar só com proxy próprio. |
| `DATABASE_URL`                 | postgres local              | Mesmo Postgres do monólito; este service só toca `sender_*` e `telegram_*`. |
| `INTERNAL_SHARED_SECRET`       | _(vazio)_                   | Segredo compartilhado com `viralefy_api`. Vazio = middleware libera (HML/dev). |
| `RESEND_API_KEY`               | _(vazio)_                   | API key do Resend. |
| `RESEND_FROM`                  | `onboarding@resend.dev`     | Remetente. |
| `RESEND_FROM_NAME`             | `Viralefy`                  | Nome amigável. |
| `RESEND_BASE_URL`              | `https://api.resend.com`    | Override pra testes. |
| `TELEGRAM_BOT_TOKEN_ENCRYPTED` | _(vazio)_                   | Bot token cifrado AES-256-GCM (fallback antes do backoffice popular `telegram_config`). |
| `TWOFA_ENCRYPTION_KEY`         | _(vazio)_                   | Hex 64 / base64 44. Reusada como key de cifragem do bot token. |
| `SENTRY_DSN`                   | _(vazio)_                   | Vazio = sentry no-op. |
| `APP_ENV`                      | _(vazio)_                   | Tag de ambiente pro Sentry. |
| `APP_VERSION`                  | `dev`                       | Injetado via `-ldflags` no build de release. |

## Build & run local

```bash
cd viralefy_sender
go build ./...
DATABASE_URL=postgres://viralefy:viralefy@localhost:15432/viralefy?sslmode=disable \
  go run ./cmd/sender
```

Health check:

```bash
curl -s http://127.0.0.1:8082/internal/health
# {"status":"ok"}
```

## Tabelas (próprias deste service)

- `sender_outbox` — fila persistente de tentativas.
- `telegram_chats` — vínculo `handle -> chat_id` capturado pelo `/start`.
- `telegram_config` — `bot_token_encrypted` + `admin_chat_id`.

Migrations vivem em `internal/infrastructure/persistence/postgres/migrations/`
e são aplicadas no boot via `embed.FS`. Numeração `001-XXX` é **isolada** do
monólito (cada microservice é dono das suas tabelas — §4 do PHASE-8).

## Layout

```
viralefy_sender/
├── cmd/sender/main.go                        # bootstrap (HTTP + outbox tick)
├── internal/
│   ├── config/config.go                      # env vars
│   ├── domain/                               # (vazio — Wave 2)
│   ├── application/outbox.go                 # Service.Tick (stub)
│   ├── infrastructure/
│   │   ├── external/email/                   # (vazio — Wave 2 move do monólito)
│   │   ├── external/telegram/                # (vazio — Wave 2)
│   │   └── persistence/postgres/
│   │       ├── db.go                         # pool pgx + RunMigrations
│   │       └── migrations/
│   │           ├── 001_sender_outbox.up/down.sql
│   │           └── 002_telegram_chats.up/down.sql
│   └── interface/http/
│       ├── router.go                         # /internal/health + /internal/send (501)
│       └── internal_auth.go                  # middleware X-Internal-Token
└── README.md
```

## Links

- [PHASE-8-MICROSERVICES.md](../viralefy_archive/PHASE-8-MICROSERVICES.md) — plano completo
- [viralefy_api](../viralefy_api/) — monólito orquestrador
- [viralefy_payments](../viralefy_payments/) — sibling (pagamentos)
- [viralefy_ops](../viralefy_ops/) — installer, systemd, Caddy
