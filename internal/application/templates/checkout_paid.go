package templates

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// PaidOrderEmailData alimenta o template "✅ Pagamento confirmado".
//
// Disparado por PaymentReceiver.MarkOrderPaid no viralefy_api (via
// /internal/v1/send com template="checkout_paid"). É o follow-up do
// e-mail de "order received": confirma que a grana caiu, repete o resumo
// do pedido e dá ETA pra ativação.
//
// SettlementAmount/SettlementCurrency vêm já formatados pelo orchestrator
// (string display-ready) — o template não faz arredondamento.
type PaidOrderEmailData struct {
	LogoURL            string
	SiteURL            string
	Year               int
	Name               string
	PlanName           string
	SettlementAmount   string
	SettlementCurrency string
	OrderShortID       string // 8 chars do uuid, ja preformatado
	CustomerEmail      string
}

const checkoutPaidHTMLTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>{{.Subject}}</title>
</head>
<body style="margin:0;padding:0;background:#0a0a0f;color:#f4f4f8;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#0a0a0f;">
<tr><td align="center" style="padding:24px;">
<table role="presentation" width="560" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;background:#14141f;border:1px solid #2a2a3d;border-radius:16px;overflow:hidden;">
  <tr><td style="background:linear-gradient(135deg,#22c55e 0%,#16a34a 100%);height:4px;line-height:4px;">&nbsp;</td></tr>
  <tr><td align="center" style="padding:28px 24px 8px;">
    <a href="{{.SiteURL}}" style="text-decoration:none;"><img src="{{.LogoURL}}" alt="Viralefy" height="32" style="height:32px;width:auto;max-width:80%;display:block;" /></a>
  </td></tr>
  <tr><td style="padding:12px 32px 24px;font-size:16px;line-height:1.6;color:#f4f4f8;">
    <h1 style="margin:8px 0 12px;font-size:22px;font-weight:700;color:#86efac;">✅ Payment confirmed</h1>
    <p style="margin:0 0 12px;color:#cbd5e1;">Hi {{.Name}}, your payment for order <strong style="color:#f4f4f8;">#{{.OrderShortID}}</strong> just landed.</p>

    <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:16px 0;background:#0a0a0f;border:1px solid #2a2a3d;border-radius:8px;">
      <tr><td style="padding:16px 18px;">
        <p style="margin:0 0 6px;color:#9ca3af;font-size:12px;letter-spacing:.5px;text-transform:uppercase;">Plan</p>
        <p style="margin:0 0 12px;font-size:18px;font-weight:600;color:#f4f4f8;">{{.PlanName}}</p>
        <p style="margin:0 0 6px;color:#9ca3af;font-size:12px;letter-spacing:.5px;text-transform:uppercase;">Charged</p>
        <p style="margin:0;font-size:20px;font-weight:700;color:#86efac;">{{.SettlementAmount}} {{.SettlementCurrency}}</p>
      </td></tr>
    </table>

    <p style="margin:16px 0 0;color:#cbd5e1;">We&rsquo;re processing your order — you&rsquo;ll see activity in your campaign within ~30 minutes. We&rsquo;ll email you again if anything needs your attention.</p>

    <hr style="border:none;border-top:1px solid #2a2a3d;margin:28px 0 16px;" />
    <p style="margin:0;font-size:13px;color:#9ca3af;text-align:center;">Receipt sent to {{.CustomerEmail}}. <a href="{{.SiteURL}}/tickets" style="color:#a855f7;text-decoration:none;">Open a ticket</a> if you have questions.</p>
  </td></tr>
  <tr><td align="center" style="padding:14px 24px;background:#0a0a0f;border-top:1px solid #2a2a3d;font-size:11px;color:#6b7280;">
    © {{.Year}} Viralefy · Responsible use of social media
  </td></tr>
</table>
</td></tr>
</table>
</body>
</html>`

var checkoutPaidTmpl = template.Must(template.New("checkout_paid").Parse(checkoutPaidHTMLTemplate))

type paidOrderEnvelope struct {
	PaidOrderEmailData
	Subject string
}

// BuildPaidOrderEmail compõe o e-mail "✅ Pagamento confirmado — order
// #{XYZ}". Aplica defaults (Year, SiteURL, LogoURL) iguais aos do
// BuildCheckoutEmail pra manter visual consistente.
func BuildPaidOrderEmail(d PaidOrderEmailData) (subject, html, text string, err error) {
	if d.Year == 0 {
		d.Year = time.Now().Year()
	}
	if d.SiteURL == "" {
		d.SiteURL = "https://viralefy.com"
	}
	if d.LogoURL == "" {
		d.LogoURL = strings.TrimRight(d.SiteURL, "/") + "/logo.png"
	}
	if d.OrderShortID == "" {
		d.OrderShortID = "—"
	}
	subject = "✅ Payment confirmed — order #" + d.OrderShortID
	env := paidOrderEnvelope{PaidOrderEmailData: d, Subject: subject}

	var buf bytes.Buffer
	if err = checkoutPaidTmpl.Execute(&buf, env); err != nil {
		return "", "", "", err
	}
	html = buf.String()
	text = renderPaidOrderText(d)
	return
}

func renderPaidOrderText(d PaidOrderEmailData) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Hi %s,\n\n", d.Name)
	fmt.Fprintf(&sb, "Your payment for order #%s just landed.\n\n", d.OrderShortID)
	fmt.Fprintf(&sb, "Plan:    %s\n", d.PlanName)
	fmt.Fprintf(&sb, "Charged: %s %s\n\n", d.SettlementAmount, d.SettlementCurrency)
	sb.WriteString("We're processing your order — you'll see activity in your campaign within ~30 minutes.\n\n")
	fmt.Fprintf(&sb, "Questions? Open a ticket at %s/tickets\n", strings.TrimRight(d.SiteURL, "/"))
	return sb.String()
}
