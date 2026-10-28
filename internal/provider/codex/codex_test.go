package codex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"subllm/internal/provider"
)

func TestChatToCodexPayload(t *testing.T) {
	sys := "Bạn là trợ lý."
	req := provider.ChatRequest{
		Model: "pa-model",
		Messages: []provider.ChatMessage{
			{Role: "system", Content: json.RawMessage(`"Bạn là trợ lý."`)},
			{Role: "user", Content: json.RawMessage(`"Xin chào"`)},
			{Role: "assistant", Content: json.RawMessage(`"Chào bạn"`)},
			{Role: "user", Content: json.RawMessage(`"1+1=?"`)},
		},
	}
	payload, err := toPayload(req)
	if err != nil {
		t.Fatalf("toPayload lỗi: %v", err)
	}
	if payload["instructions"] != sys {
		t.Fatalf("instructions = %v, muốn %q", payload["instructions"], sys)
	}
	if payload["stream"] != true || payload["store"] != false {
		t.Fatalf("phải stream:true store:false, nhận %v/%v", payload["stream"], payload["store"])
	}
	input := payload["input"].([]map[string]any)
	if len(input) != 3 {
		t.Fatalf("input phải 3 item (system gộp vào instructions), nhận %d", len(input))
	}
	if input[0]["role"] != "user" {
		t.Fatalf("item đầu phải user, nhận %v", input[0]["role"])
	}
	content := input[0]["content"].([]map[string]any)
	if content[0]["type"] != "input_text" {
		t.Fatalf("user phải input_text, nhận %v", content[0]["type"])
	}
	if input[1]["role"] != "assistant" {
		t.Fatalf("item hai phải assistant, nhận %v", input[1]["role"])
	}
}

func TestCodexEvent(t *testing.T) {
	ev, err := decodeEvent([]byte(`{"type":"response.output_text.delta","delta":"Xin"}`))
	if err != nil || ev.Text != "Xin" {
		t.Fatalf("delta sai: %+v err=%v", ev, err)
	}
	ev, err = decodeEvent([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`))
	if err != nil || !ev.Done {
		t.Fatalf("completed sai: %+v err=%v", ev, err)
	}
	if ev.Usage == nil || ev.Usage.PromptTokens != 10 || ev.Usage.TotalTokens != 15 {
		t.Fatalf("usage codex sai: %+v", ev.Usage)
	}
	if _, err := decodeEvent([]byte(`{"type":"response.failed"}`)); err == nil {
		t.Fatal("response.failed phải là lỗi")
	}
	// sự kiện bỏ qua: created, in_progress…
	ev, _ = decodeEvent([]byte(`{"type":"response.created"}`))
	if ev.Text != "" || ev.Done {
		t.Fatalf("sự kiện vô hại phải rỗng: %+v", ev)
	}
}

// reasoning_effort → "reasoning": {"effort": …} của Responses API; không gửi
// thì body giữ nguyên như cũ (không có khoá reasoning).
func TestCodexReasoningEffort(t *testing.T) {
	req := provider.ChatRequest{Model: "pa-model", Messages: []provider.ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
	payload, err := toPayload(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := payload["reasoning"]; has {
		t.Fatalf("không gửi effort thì không được có khoá reasoning: %v", payload["reasoning"])
	}
	req.ReasoningEffort = "e-x" // giá trị trung tính: code không được giữ danh sách mức
	payload, err = toPayload(req)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := payload["reasoning"].(map[string]any)
	if !ok || r["effort"] != "e-x" || len(r) != 1 {
		t.Fatalf(`reasoning phải là {"effort":"e-x"}, nhận %#v`, payload["reasoning"])
	}
}

// Request không dịch được bị chặn TRƯỚC khi đụng credential (giống claude).
func TestBadRequestCheckedBeforeCredentials(t *testing.T) {
	p := New(Config{CredFile: "/nonexistent/auth.json"})
	req := provider.ChatRequest{Model: "pa-model", Messages: []provider.ChatMessage{{Role: "tool", Content: json.RawMessage(`"x"`)}}}
	err := p.StreamChat(context.Background(), req, func(provider.StreamEvent) error { return nil })
	var re *provider.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("phải là RequestError (kiểm request trước credential), nhận %T: %v", err, err)
	}
}
