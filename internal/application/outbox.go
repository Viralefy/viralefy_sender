// Package application abriga os casos de uso do viralefy_sender.
//
// Por ora só o Service do outbox, em forma de stub: Tick é no-op e fica
// pronto pra ganhar a lógica real no Wave 2 (read batch enqueued → dispatch
// via adapter de email/telegram → atualizar status + exponential backoff:
// 30s → 5min → 1h → 6h → 24h, máx 5 tentativas conforme §2 do PHASE-8).
package application

import (
	"context"
	"log/slog"
)

// Service orquestra a fila persistente (sender_outbox). No scaffold ele
// não tem dependências reais — vai ganhar repo + email/telegram clients
// no Wave 2.
type Service struct {
	Logger *slog.Logger
}

// NewService cria o orchestrator do outbox. Mantemos o construtor mesmo
// sem campos efetivos pra cristalizar o ponto de injeção quando o repo
// chegar.
func NewService(logger *slog.Logger) *Service {
	return &Service{Logger: logger}
}

// Tick é chamado pelo cron a cada 30s (ver cmd/sender/main.go). No stub
// só loga — o batch real vem no Wave 2.
func (s *Service) Tick(_ context.Context) {
	if s.Logger != nil {
		s.Logger.Info("outbox tick", "status", "stub", "processed", 0)
	}
}
