package server

// types.go — kiểu phản hồi mặt OpenAI chat completions: đây là "mặt tiền"
// subllm trả ra ngoài, chỉ server dùng.

import "subllm/internal/provider"

// deltaBody — phần thay đổi trong một chunk stream.
type deltaBody struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type chunkChoice struct {
	Index        int       `json:"index"`
	Delta        deltaBody `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

// chunk — một sự kiện stream theo định dạng OpenAI (chat.completion.chunk).
type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
}

type messageOut struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type choiceOut struct {
	Index        int        `json:"index"`
	Message      messageOut `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

// completion — phản hồi non-stream theo định dạng OpenAI.
type completion struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []choiceOut    `json:"choices"`
	Usage   provider.Usage `json:"usage"`
}

// apiError — lỗi trả về cho client, bám định dạng lỗi của OpenAI.
type apiError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func errJSON(message, typ string) apiError {
	e := apiError{}
	e.Error.Message = message
	e.Error.Type = typ
	return e
}
