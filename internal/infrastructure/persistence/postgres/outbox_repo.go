// Package postgres implementa application.OutboxRepo sobre pgx/pgxpool.
//
// O ciclo do row é o do §2 do PHASE-8:
//
//	Enqueue        → INSERT ON CONFLICT (attempt_id) DO NOTHING
//	LockBatch      → BEGIN; SELECT ... FOR UPDATE SKIP LOCKED;
//	                 UPDATE status=in_flight; COMMIT
//	MarkSent       → UPDATE status=sent
//	MarkRetry      → UPDATE attempt_count+=1, status=enqueued,
//	                 next_attempt_at=NOW()+backoff
//	MarkFailedFinal→ UPDATE status=failed_final
//
// FOR UPDATE SKIP LOCKED garante que múltiplas réplicas do sender (futuro
// HA) não disputem os mesmos rows. Por ora rodamos 1 réplica, mas a query
// já está pronta.
package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Viralefy/viralefy_sender/internal/application"
)

// OutboxRepo é o adapter pg da fila. Construído no main do sender.
type OutboxRepo struct {
	pool *pgxpool.Pool
}

// NewOutboxRepo devolve um repo pronto. pool não pode ser nil.
func NewOutboxRepo(pool *pgxpool.Pool) *OutboxRepo {
	return &OutboxRepo{pool: pool}
}

// Enqueue insere row novo. ON CONFLICT em attempt_id (UNIQUE) faz NOTHING,
// e devolvemos created=false — caller (Service) sabe que foi idempotency
// hit e nao deve dispatchar de novo.
func (r *OutboxRepo) Enqueue(ctx context.Context, row application.OutboxRow) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO sender_outbox (
			id, attempt_id, channel, template,
			recipient_email, recipient_telegram, vars,
			status, attempt_count, next_attempt_at
		) VALUES (
			$1, $2, $3, $4,
			NULLIF($5, ''), NULLIF($6, ''), $7,
			'enqueued', 0, $8
		)
		ON CONFLICT (attempt_id) DO NOTHING
	`,
		row.ID, row.AttemptID, row.Channel, row.Template,
		row.RecipientEmail, row.RecipientTelegram, row.Vars,
		row.NextAttemptAt,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// LockBatch puxa até `limit` rows elegíveis e os marca como in_flight
// dentro do mesmo tx. O lock é solto no COMMIT — chamadas subsequentes
// veem status=in_flight e ignoram (índice cobre só status=enqueued).
func (r *OutboxRepo) LockBatch(ctx context.Context, limit int) ([]application.OutboxRow, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, attempt_id, channel, template,
		       COALESCE(recipient_email, ''), COALESCE(recipient_telegram, ''),
		       vars, status, attempt_count, COALESCE(last_error, ''),
		       next_attempt_at, created_at, updated_at
		FROM sender_outbox
		WHERE status = 'enqueued' AND next_attempt_at <= NOW()
		ORDER BY next_attempt_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return nil, err
	}

	var out []application.OutboxRow
	ids := make([]string, 0, limit)
	for rows.Next() {
		var row application.OutboxRow
		if err := rows.Scan(
			&row.ID, &row.AttemptID, &row.Channel, &row.Template,
			&row.RecipientEmail, &row.RecipientTelegram,
			&row.Vars, &row.Status, &row.AttemptCount, &row.LastError,
			&row.NextAttemptAt, &row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, row)
		ids = append(ids, row.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		// Commit pra não deixar tx pendurado.
		return out, tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE sender_outbox
		SET status = 'in_flight', updated_at = NOW()
		WHERE id = ANY($1::text[])
	`, ids); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// MarkSent fecha o row em sent. Idempotente (UPDATE com WHERE status<>'sent'
// seria mais defensivo, mas o lock no tick garante exclusividade).
func (r *OutboxRepo) MarkSent(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE sender_outbox
		SET status = 'sent', last_error = NULL, updated_at = NOW()
		WHERE id = $1
	`, id)
	return err
}

// MarkRetry volta o row pra enqueued, incrementa attempt_count e agenda
// next_attempt_at = NOW()+backoff. last_error guarda o motivo da última
// falha pra debugging do admin.
func (r *OutboxRepo) MarkRetry(ctx context.Context, id, lastErr string, backoff time.Duration) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE sender_outbox
		SET status = 'enqueued',
		    attempt_count = attempt_count + 1,
		    last_error = $2,
		    next_attempt_at = NOW() + $3::interval,
		    updated_at = NOW()
		WHERE id = $1
	`, id, lastErr, backoff)
	return err
}

// MarkFailedFinal fecha o row em failed_final — esgotou tentativas.
// Alerta admin é responsabilidade do Service.Logger (caller).
func (r *OutboxRepo) MarkFailedFinal(ctx context.Context, id, lastErr string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE sender_outbox
		SET status = 'failed_final',
		    attempt_count = attempt_count + 1,
		    last_error = $2,
		    updated_at = NOW()
		WHERE id = $1
	`, id, lastErr)
	return err
}
