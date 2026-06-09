// Package postgres encapsula o pool pgx e a execução de migrations embarcadas.
//
// O wrapper é deliberadamente idêntico ao do viralefy_api: um único `DB` que
// expõe `Pool()` pra repositórios + `RunMigrations` lendo de embed.FS. Manter
// a forma 1:1 facilita extrair os repos do monolito no Wave 2 sem mexer em
// assinaturas.
package postgres

import (
	"context"
	"embed"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type DB struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close() { d.pool.Close() }

func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// RunMigrations aplica todos os arquivos *.up.sql em ordem lexicográfica.
// Numeração 001-XXX é isolada do monolito (cada service é dono das suas
// tabelas) — conforme §4 do PHASE-8.
func RunMigrations(ctx context.Context, db *DB) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if _, err := db.pool.Exec(ctx, string(b)); err != nil {
			return err
		}
	}
	return nil
}
