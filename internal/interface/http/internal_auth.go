// Package http expõe os handlers internos do viralefy_sender.
//
// Defesa-em-profundidade: o service já está atrás de loopback (127.0.0.1),
// mas todo request em /internal/* precisa carregar X-Internal-Token igual a
// INTERNAL_SHARED_SECRET. Compara em tempo constante (subtle.ConstantTimeCompare)
// pra evitar timing leak — paranóia de placebo num loopback, mas custo zero
// e fica idêntico ao middleware do payments.
package http

import (
	"crypto/subtle"
	"net/http"
)

// InternalAuth devolve um middleware chi-compatível que valida o header
// X-Internal-Token contra o segredo configurado. Quando o segredo está
// vazio (HML/dev sem installer rodado), o middleware loga e segue — não
// queremos travar o boot inicial num ambiente que ainda não foi provisionado.
func InternalAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if secret == "" {
				next.ServeHTTP(w, r)
				return
			}
			got := r.Header.Get("X-Internal-Token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
