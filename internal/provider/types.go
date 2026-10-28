package provider

// types.go — kiểu dữ liệu mặt OpenAI chat completions mà server và mọi
// strategy cùng dùng (request vào + usage). Kiểu phản hồi ra nằm ở server.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ChatMessage — một tin nhắn theo OpenAI chat completions. Content có thể
// là string hoặc mảng part (text · image_url · file); đọc qua Parts, hoặc
// ContentText cho vai chỉ có chữ.
type ChatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content,omitempty"`
}

// Loại Part sau khi chuẩn hoá.
const (
	PartText  = "text"
	PartImage = "image"
	PartFile  = "file"
)

// Part — một phần nội dung đã chuẩn hoá, trung lập với backend.
type Part struct {
	Type     string // PartText | PartImage | PartFile
	Text     string // PartText
	URL      string // PartImage: data URI hoặc http(s) URL; PartFile: data URI (file_data)
	Filename string // PartFile
}

// Parts — đọc content dạng string hoặc mảng part chuẩn OpenAI, giữ thứ tự.
// Kiểu chưa hỗ trợ hay part thiếu dữ liệu → lỗi (client nhận 400), không
// lặng lẽ bỏ: bỏ ảnh thì model vẫn trả lời 200 như chưa từng thấy ảnh.
func (m ChatMessage) Parts() ([]Part, error) {
	if len(m.Content) == 0 {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return []Part{{Type: PartText, Text: s}}, nil
	}
	var raw []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
		File struct {
			FileData string `json:"file_data"`
			Filename string `json:"filename"`
		} `json:"file"`
	}
	if err := json.Unmarshal(m.Content, &raw); err != nil {
		return nil, fmt.Errorf("content không phải string hay mảng part: %w", err)
	}
	parts := make([]Part, 0, len(raw))
	for i, p := range raw {
		switch p.Type {
		case "text", "":
			if p.Type == "" && (p.Text == "" || p.ImageURL.URL != "" || p.File.FileData != "") {
				// coi là chữ thì ảnh/file/audio rơi lặng lẽ; part chỉ có chữ thì vẫn nhận
				return nil, fmt.Errorf("content[%d]: part thiếu \"type\" (ảnh: \"image_url\", file: \"file\")", i)
			}
			parts = append(parts, Part{Type: PartText, Text: p.Text})
		case "image_url":
			if p.ImageURL.URL == "" {
				return nil, fmt.Errorf("content[%d]: image_url.url trống", i)
			}
			parts = append(parts, Part{Type: PartImage, URL: p.ImageURL.URL})
		case "file":
			if p.File.FileData == "" {
				return nil, fmt.Errorf("content[%d]: file cần file_data (data URI base64); file_id chưa hỗ trợ", i)
			}
			parts = append(parts, Part{Type: PartFile, URL: p.File.FileData, Filename: p.File.Filename})
		default:
			return nil, fmt.Errorf("content[%d]: part %q chưa hỗ trợ (chỉ text, image_url, file)", i, p.Type)
		}
	}
	return parts, nil
}

// ContentText — text của tin nhắn chỉ có chữ (system, assistant); gặp ảnh
// hay file → lỗi.
func (m ChatMessage) ContentText() (string, error) {
	parts, err := m.Parts()
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, p := range parts {
		if p.Type != PartText {
			return "", fmt.Errorf("role %q chỉ nhận text, không nhận %s", m.Role, p.Type)
		}
		out.WriteString(p.Text)
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
