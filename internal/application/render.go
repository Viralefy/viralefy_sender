package application

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Viralefy/viralefy_sender/internal/application/templates"
)

// renderEmail materializa um OutboxRow em (subject, html, text) chamando o
// builder do template apropriado. Cada template tem uma forma de Vars
// específica — desserializamos via json.Unmarshal pra struct tipada pra
// não ficar dependente de map[string]interface{} em runtime.
//
// Templates suportados (PHASE-8 §6):
//   - "checkout"        — order received (pré pagamento)
//   - "checkout_paid"   — pagamento confirmado
//   - "proof_rejected"  — comprovante recusado
func renderEmail(row OutboxRow) (subject, html, text string, err error) {
	switch row.Template {
	case "":
		// Raw passthrough — monolith pré-renderizou subject + body e
		// stashou em vars._raw_*. Não há rendering a fazer, só leitura.
		var raw map[string]interface{}
		if err = unmarshalVars(row.Vars, &raw); err != nil {
			return
		}
		if s, _ := raw["_raw_subject"].(string); s != "" {
			subject = s
		}
		if h, _ := raw["_raw_html"].(string); h != "" {
			html = h
		}
		if t, _ := raw["_raw_text"].(string); t != "" {
			text = t
		}
		if subject == "" || (html == "" && text == "") {
			err = fmt.Errorf("raw email passthrough: missing subject or body")
		}
		return

	case "checkout":
		var d templates.CheckoutEmailData
		if err = unmarshalVars(row.Vars, &d); err != nil {
			return
		}
		if d.Name == "" {
			d.Name = "there"
		}
		return templates.BuildCheckoutEmail(d)

	case "checkout_paid":
		var d templates.PaidOrderEmailData
		if err = unmarshalVars(row.Vars, &d); err != nil {
			return
		}
		if d.CustomerEmail == "" {
			d.CustomerEmail = row.RecipientEmail
		}
		if d.Name == "" {
			d.Name = "there"
		}
		return templates.BuildPaidOrderEmail(d)

	case "proof_rejected":
		var d templates.ProofRejectedEmailData
		if err = unmarshalVars(row.Vars, &d); err != nil {
			return
		}
		if d.To == "" {
			d.To = row.RecipientEmail
		}
		if d.Name == "" {
			d.Name = "there"
		}
		return templates.BuildProofRejectedEmail(d)

	default:
		return "", "", "", fmt.Errorf("unknown email template %q", row.Template)
	}
}

// renderTelegram devolve (text, parseMode) pra SendMessage. Por ora cobrimos
// dois templates Markdown V2 conforme §6 do PHASE-8 — outros (admin alert,
// proof_rejected_telegram) ficam pra Wave 3.
func renderTelegram(row OutboxRow) (text, parseMode string, err error) {
	vars := map[string]string{}
	if len(row.Vars) > 0 {
		raw := map[string]interface{}{}
		if err = json.Unmarshal(row.Vars, &raw); err != nil {
			return "", "", fmt.Errorf("decode vars: %w", err)
		}
		for k, v := range raw {
			vars[k] = fmt.Sprint(v)
		}
	}
	switch row.Template {
	case "checkout_paid":
		var sb strings.Builder
		sb.WriteString("✅ *Payment confirmed*\n")
		fmt.Fprintf(&sb, "Order #%s — %s\n", mvEscape(vars["order_short_id"]), mvEscape(vars["plan_name"]))
		sb.WriteString("We're processing — update in up to 30min\\.")
		return sb.String(), "MarkdownV2", nil

	case "checkout_paid_admin":
		var sb strings.Builder
		sb.WriteString("💰 *New sale*\n")
		fmt.Fprintf(&sb, "Plan: %s\n", mvEscape(vars["plan_name"]))
		fmt.Fprintf(&sb, "Amount: %s %s\n", mvEscape(vars["settlement_amount"]), mvEscape(vars["settlement_currency"]))
		fmt.Fprintf(&sb, "Customer: %s\n", mvEscape(vars["customer_email"]))
		fmt.Fprintf(&sb, "Order: %s", mvEscape(vars["order_short_id"]))
		return sb.String(), "MarkdownV2", nil

	default:
		return "", "", fmt.Errorf("unknown telegram template %q", row.Template)
	}
}

// unmarshalVars desserializa o JSONB cru do row pra struct tipada.
// Aceita Vars vazio (== nil) tratando como "{}".
func unmarshalVars(raw []byte, out interface{}) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode vars: %w", err)
	}
	return nil
}

// mvEscape escapa caracteres reservados do MarkdownV2 do Telegram.
// Lista oficial: _*[]()~`>#+-=|{}.!
// Sem escape, o Bot API devolve 400 e o tick falha o row.
func mvEscape(s string) string {
	if s == "" {
		return ""
	}
	const special = "_*[]()~`>#+-=|{}.!\\"
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
