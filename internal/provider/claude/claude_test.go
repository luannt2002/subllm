package claude

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"subllm/internal/provider"
)

func TestChatToClaudePayload(t *testing.T) {
	req := provider.ChatRequest{
		Model: "pb-model",
		Messages: []provider.ChatMessage{
			{Role: "system", Content: json.RawMessage(`"Hệ A."`)},
			{Role: "system", Content: json.RawMessage(`"Hệ B."`)},
			{Role: "user", Content: json.RawMessage(`"Hi"`)},
		},
	}
	p := New(Config{MaxTokens: 1234}) // khác hằng 4096 cũ: chứng minh đọc từ Config
	payload, err := p.toPayload(req)
	if err != nil {
		t.Fatalf("toPayload lỗi: %v", err)
	}
	if payload["system"] != "Hệ A.\n\nHệ B." {
		t.Fatalf("system ghép sai: %v", payload["system"])
	}
	if payload["max_tokens"] != 1234 {
		t.Fatalf("max_tokens phải lấy mặc định từ Config (1234), nhận %v", payload["max_tokens"])
	}
	msgs := payload["messages"].([]map[string]any)
	if len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Fatalf("messages sai: %v", msgs)
	}
	// client gửi max_tokens thì dùng của client
	n := 100
	req.MaxTokens = &n
	payload, _ = p.toPayload(req)
	if payload["max_tokens"] != 100 {
		t.Fatalf("max_tokens client gửi phải được tôn trọng, nhận %v", payload["max_tokens"])
	}
}

func TestClaudeEvent(t *testing.T) {
	ev, err := decodeEvent([]byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"chào"}}`))
	if err != nil || ev.Text != "chào" {
		t.Fatalf("delta sai: %+v err=%v", ev, err)
	}
	ev, _ = decodeEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":7}}}`))
	if ev.Usage == nil || ev.Usage.PromptTokens != 7 {
		t.Fatalf("message_start usage sai: %+v", ev.Usage)
	}
	ev, _ = decodeEvent([]byte(`{"type":"message_delta","usage":{"output_tokens":3}}`))
	if ev.Usage == nil || ev.Usage.CompletionTokens != 3 {
		t.Fatalf("message_delta usage sai: %+v", ev.Usage)
	}
	ev, _ = decodeEvent([]byte(`{"type":"message_stop"}`))
	if !ev.Done {
		t.Fatal("message_stop phải Done")
	}
	if _, err := decodeEvent([]byte(`{"type":"error","error":{"message":"rate limited"}}`)); err == nil {
		t.Fatal("error event phải là lỗi")
	}
}

// Kiểu claude chưa hỗ trợ effort: phải lỗi rõ (400), không lặng lẽ bỏ qua.
func TestClaudeRejectsReasoningEffort(t *testing.T) {
	p := New(Config{MaxTokens: 1234})
	req := provider.ChatRequest{Model: "pb-model", ReasoningEffort: "e-x", Messages: []provider.ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
	if _, err := p.toPayload(req); err == nil || !strings.Contains(err.Error(), "reasoning_effort") {
		t.Fatalf("claude nhận reasoning_effort phải lỗi nêu tên field, nhận %v", err)
	}
}

// Request sai bị chặn TRƯỚC khi đụng credential: file cred hỏng vẫn ra lỗi
// request (400), không kích refresh, không thành lỗi tài khoản.
func TestBadRequestCheckedBeforeCredentials(t *testing.T) {
	p := New(Config{CredFile: "/nonexistent/cred.json", MaxTokens: 1})
	req := provider.ChatRequest{Model: "pb-model", ReasoningEffort: "e-x", Messages: []provider.ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
	err := p.StreamChat(context.Background(), req, func(provider.StreamEvent) error { return nil })
	var re *provider.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("phải là RequestError (kiểm request trước credential), nhận %T: %v", err, err)
	}
}
