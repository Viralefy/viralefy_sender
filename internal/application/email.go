// Package application abriga os casos de uso do viralefy_sender.
//
// Este arquivo define a porta de saída EmailSender + o modelo EmailMessage.
// A implementação concreta (Resend/SMTP/Log) vive em
// internal/infrastructure/external/email — duplicada do monolito por design
// pra que o sender seja standalone (sem dep cycle com viralefy_api).
package application

import "context"

// EmailMessage é a mensagem de e-mail no modelo da aplicação.
type EmailMessage struct {
	To       string
	Subject  string
	HTMLBody string
	TextBody string
}

// EmailSender é a porta de saída para envio de e-mail. A implementação
// concreta vive em infrastructure/external/email.
type EmailSender interface {
	Send(ctx context.Context, msg EmailMessage) error
}
