package provider

// types.go — kiểu dữ liệu mặt OpenAI chat completions mà server và mọi
// strategy cùng dùng (request vào + usage). Kiểu phản hồi ra nằm ở server.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ChatMessage — một tin nhắn theo OpenAI chat completions. Content có thể
// là string hoặc mảng part ({type,text}); đọc qua ContentText.
type ChatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content,omitempty"`
}

// ContentText — rút text từ content dạng string hoặc mảng part.
func (m ChatMessage) ContentText() (string, error) {
	if len(m.Content) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return "", fmt.Errorf("content không phải string hay mảng part: %w", err)
	}
	var out strings.Builder
	for _, p := range parts {
		if p.Type == "text" || p.Type == "" {
			out.WriteString(p.Text)
		}
	}
	return out.String(), nil
}

// ChatRequest — body của POST /v1/chat/completions. Chỉ nhận những field
// subllm hiểu; field lạ bị bỏ qua. Provider là tên một provider (vd
// "codex"), tuỳ chọn: đặt đích danh thì thắng việc route theo
// tiền tố model.
type ChatRequest struct {
	Model     string        `json:"model"`
	Provider  string        `json:"provider,omitempty"`
	Messages  []ChatMessage `json:"messages"`
	Stream    bool          `json:"stream"`
	MaxTokens *int          `json:"max_tokens,omitempty"`
	// ReasoningEffort — mức suy nghĩ (tên field chuẩn OpenAI chat
	// completions). Chuyển nguyên cho backend: mức hợp lệ tuỳ model, do
	// backend kiểm; rỗng = mặc định của model.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// Usage — token tiêu thụ, định dạng OpenAI.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Add — cộng dồn usage của một sự kiện vào tổng.
func (u Usage) Add(other Usage) Usage {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	return u
}
