package application

import "github.com/google/uuid"

// newUUID gera UUIDv4 — wrapper trivial pra que defaultUUID (em outbox.go)
// não precise importar google/uuid diretamente e fique injetável em testes
// via Service.IDGen.
//
// Por que UUID e não snowflake/ULID? Compatibilidade com attempt_id do
// viralefy_api (já é UUID lá) e zero deps novas (pgx já puxa google/uuid).
func newUUID() string {
	return uuid.NewString()
}
