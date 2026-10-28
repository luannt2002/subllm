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
	if c := input[1]["content"].([]map[string]any); c[0]["type"] != "output_text" || c[0]["text"] != "Chào bạn" {
		t.Fatalf("assistant phải output_text giữ nguyên chữ, nhận %v", c)
	}
}

// Part ảnh/file của OpenAI → input_image/input_file của Responses API, giữ thứ tự.
func TestCodexMultimodalPayload(t *testing.T) {
	req := provider.ChatRequest{Model: "pa-model", Messages: []provider.ChatMessage{{Role: "user", Content: json.RawMessage(`[
		{"type":"text","text":"so sánh"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}},
		{"type":"image_url","image_url":{"url":"https://x/b.jpg"}},
		{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,BBB"}}]`)}}}
	payload, err := toPayload(req)
	if err != nil {
		t.Fatal(err)
	}
	content := payload["input"].([]map[string]any)[0]["content"].([]map[string]any)
	want := []map[string]any{
		{"type": "input_text", "text": "so sánh"},
		{"type": "input_image", "image_url": "data:image/png;base64,AAA"},
		{"type": "input_image", "image_url": "https://x/b.jpg"},
		{"type": "input_file", "filename": "a.pdf", "file_data": "data:application/pdf;base64,BBB"},
	}
	got, _ := json.Marshal(content)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("content sai:\n nhận %s\n muốn %s", got, exp)
	}
	// system chỉ nhận chữ: ảnh ở system phải 400, không lặng lẽ bỏ.
	req.Messages[0].Role = "system"
	if _, err := toPayload(req); err == nil {
		t.Fatal("ảnh trong system phải lỗi")
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
	_, err = decodeEvent([]byte(`{"type":"response.failed"}`))
	var re *provider.RequestError
	if err == nil || errors.As(err, &re) {
		t.Fatalf("response.failed phải là lỗi backend (502), không phải lỗi request: %T %v", err, err)
	}
	// Lỗi request báo giữa stream (payload thật: backend không tải được link
	// ảnh) → RequestError → client 400, không failover.
	_, err = decodeEvent([]byte(`{"type":"error","error":{"message":"Error while downloading file. Upstream status code: 400.","type":"invalid_request_error","param":"url","code":"invalid_value"}}`))
	if !errors.As(err, &re) {
		t.Fatalf("invalid_request_error phải là RequestError, nhận %T %v", err, err)
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
