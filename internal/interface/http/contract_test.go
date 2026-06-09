package http

// Contract test do lado servidor — espelha
// viralefy_api/internal/infrastructure/external/senderclient/client_test.go.
//
// Serializa as mesmas structs que o handler emite + desserializa o body que
// o client envia, validando o envelope JSON em ambas direções.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Viralefy/viralefy_sender/internal/application"
)

// Fixture compartilhada (deve bater com FixtureSendResponse no client).
const expectedResponseShape = `{"status":"queued","attempt_id":"att_01H8XYZ1234"}`

func TestSendResponseShape_MatchesClient(t *testing.T) {
	got, err := json.Marshal(sendResponse{
		Status:    "queued",
		AttemptID: "att_01H8XYZ1234",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(got), `"status":"queued"`) {
		t.Errorf("status tag mudou — client espera 'status'\ngot=%s", string(got))
	}
	if !strings.Contains(string(got), `"attempt_id":"att_01H8XYZ1234"`) {
		t.Errorf("attempt_id tag mudou — client extrai pelo nome exato\ngot=%s", string(got))
	}

	// Roundtrip pra struct espelho do client.
	var mirror struct {
		Status    string `json:"status"`
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal(got, &mirror); err != nil {
		t.Fatalf("unmarshal mirror: %v", err)
	}
	if mirror.Status == "" || mirror.AttemptID == "" {
		t.Errorf("mirror perdeu campos — DRIFT: %+v", mirror)
	}
}

// TestRawPassthroughBody confirma que o servidor desserializa o body que
// o client manda em raw mode (sem Template, com Subject+HTMLBody/TextBody).
// Bug histórico: sender 400 "template required" → "checkout: send email
// failed" em prod. Fix: aceitar raw passthrough quando Template vazio.
func TestRawPassthroughBody_DeserializesCleanly(t *testing.T) {
	// Body exatamente como o senderclient.Send monta no monolith.
	raw := `{
		"channel": "email",
		"to": {"email": "buyer@example.com"},
		"subject": "Confirmação do pedido",
		"html_body": "<p>Obrigado pela compra</p>",
		"text_body": "Obrigado pela compra",
		"priority": "normal"
	}`
	var got application.SendRequest
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal raw passthrough: %v", err)
	}
	if got.Channel != "email" {
		t.Errorf("channel %q", got.Channel)
	}
	if got.To.Email != "buyer@example.com" {
		t.Errorf("to.email %q", got.To.Email)
	}
	if got.Subject != "Confirmação do pedido" {
		t.Errorf("subject %q", got.Subject)
	}
	if got.HTMLBody == "" {
		t.Error("html_body vazia — tag divergiu (renderer não tem o que mandar)")
	}
	if got.TextBody == "" {
		t.Error("text_body vazia — tag divergiu")
	}
	if got.Template != "" {
		t.Errorf("Template deveria ser vazio em raw mode, got %q", got.Template)
	}
}

// TestTemplateModeBody cobre o cenário moderno: template + vars apenas.
func TestTemplateModeBody_DeserializesCleanly(t *testing.T) {
	raw := `{
		"channel": "email",
		"template": "checkout_paid",
		"to": {"email": "buyer@example.com"},
		"vars": {"order_short_id": "ORD-12345", "plan_name": "1000 followers"},
		"priority": "normal"
	}`
	var got application.SendRequest
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal template mode: %v", err)
	}
	if got.Template != "checkout_paid" {
		t.Errorf("template %q", got.Template)
	}
	if got.Vars["order_short_id"] != "ORD-12345" {
		t.Errorf("vars.order_short_id %v", got.Vars["order_short_id"])
	}
	if got.Subject != "" || got.HTMLBody != "" || got.TextBody != "" {
		t.Errorf("template mode não deveria popular raw fields: %+v", got)
	}
}

// TestTelegramChannelBody — telegram_handle no to, sem email.
func TestTelegramChannelBody_DeserializesCleanly(t *testing.T) {
	raw := `{
		"channel": "telegram",
		"template": "checkout_paid",
		"to": {"telegram_handle": "@buyer"},
		"vars": {"order_short_id": "ORD-9", "plan_name": "Test"}
	}`
	var got application.SendRequest
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal telegram: %v", err)
	}
	if got.Channel != "telegram" {
		t.Errorf("channel %q", got.Channel)
	}
	if got.To.TelegramHandle != "@buyer" {
		t.Errorf("to.telegram_handle %q", got.To.TelegramHandle)
	}
	if got.To.Email != "" {
		t.Errorf("email não deveria ser populado em telegram channel: %q", got.To.Email)
	}
}
