package templates

import (
	"html"
	"strings"
)

// ProofRejectedEmailData alimenta o email "comprovante precisa de revisão".
//
// Migrado do buildProofRejectionEmail do monolito (handlers.go:2412) com
// uma pequena melhoria: Reason é HTML-escaped no template, evitando que
// nota do admin com `<script>` vire XSS no client de email que renderiza.
type ProofRejectedEmailData struct {
	Name         string
	To           string
	OrderShortID string // 8 chars do uuid (caller pode já truncar)
	Reason       string // opcional — vazio = bloco "reviewer note" some
}

// BuildProofRejectedEmail devolve subject/html/text pro template
// "proof_rejected". Deliberadamente simples: nada de imagens nem CTA
// styled — é uma mensagem de fricção, queremos o cliente tomando ação,
// não admirando o layout.
func BuildProofRejectedEmail(d ProofRejectedEmailData) (subject, htmlBody, text string, err error) {
	short := d.OrderShortID
	if len(short) > 8 {
		short = short[:8]
	}
	reasonBlock := ""
	if r := strings.TrimSpace(d.Reason); r != "" {
		reasonBlock = "<p><strong>Reviewer note:</strong> " + html.EscapeString(r) + "</p>"
	}
	safeName := html.EscapeString(d.Name)
	htmlBody = "<p>Hi " + safeName + ",</p>" +
		"<p>We couldn&rsquo;t verify the payment proof you uploaded for order <strong>#" + html.EscapeString(short) + "</strong>.</p>" +
		reasonBlock +
		"<p>Please open the order in your account and re-upload a clearer screenshot or the transaction hash. We&rsquo;ll activate the order as soon as we can confirm the deposit.</p>" +
		"<p>— Viralefy</p>"

	text = "Hi " + d.Name + ",\n\nWe couldn't verify the payment proof you uploaded for order #" + short + ".\n"
	if r := strings.TrimSpace(d.Reason); r != "" {
		text += "Reviewer note: " + r + "\n"
	}
	text += "\nPlease re-upload a clearer screenshot or your transaction hash from your account.\n\n— Viralefy"

	subject = "Payment proof needs another look — Order #" + short
	return subject, htmlBody, text, nil
}
