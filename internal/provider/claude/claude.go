// Package claude — strategy backend Anthropic Messages API bằng đăng nhập
// Claude Code (gói Pro/Max) — thử nghiệm: token + /v1/models đã chạy trên máy,
// chat chưa (xem README). Luồng:
//   - token: file credential của Claude Code → object "claudeAiOauth"
//     {accessToken, refreshToken, expiresAt(ms)}; refresh dạng JSON body với
//     client_id của Claude Code;
//   - gọi: POST {base}/v1/messages?beta=true với Authorization: Bearer +
//     header anthropic-beta (OAuth), KHÔNG phải x-api-key;
//   - stream: sự kiện content_block_delta mang text; usage nằm ở
//     message_start (input) và message_delta (output).
//
// Endpoint, client ID, phiên bản… không nằm trong code: config.go đọc từ env
// (mẫu ở .env.example); nhiều tài khoản = nhiều provider cùng kiểu claude.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"subllm/internal/provider"
	"subllm/internal/tokenstore"
)

// Config — cấu hình một provider kiểu claude; config.go đọc từng field từ
// env.
type Config struct {
	Name        string // tên provider (ID, field "provider" trong body)
	CredFile    string // file credential của Claude Code
	ModelPrefix string // model mang tiền tố này route về Claude
	BaseURL     string // gốc API, ghép thêm /v1/messages, /v1/models
	TokenURL    string // endpoint refresh OAuth
	ClientID    string // client ID công khai của Claude Code
	APIVersion  string // header anthropic-version
	Beta        string // header anthropic-beta
	UserAgent   string // header User-Agent
	// MaxTokens — backend BẮT BUỘC max_tokens; dùng khi client không gửi.
	MaxTokens int
	// Timeout — trần một request upstream, tính cả lúc đọc stream.
	Timeout        time.Duration
	RefreshMargin  time.Duration // refresh khi token còn hạn dưới mức này
	RefreshTimeout time.Duration // trần một lượt refresh
}

// Provider — strategy Claude, cài provider.Provider.
type Provider struct {
	cfg  Config
	cred *tokenstore.Store
	http *http.Client
}

// New — strategy Claude theo cfg.
func New(cfg Config) *Provider {
	c := tokenstore.New(cfg.CredFile, "claudeAiOauth", cfg.RefreshMargin, cfg.RefreshTimeout)
	c.AccessKey = "accessToken"
	c.RefreshKey = "refreshToken"
	c.ExpiresAtMsKey = "expiresAt"
	c.RefreshURL = cfg.TokenURL
	c.ClientID = cfg.ClientID
	c.JSONBody = true
	return &Provider{cfg: cfg, cred: c, http: &http.Client{Timeout: cfg.Timeout}}
}

func (p *Provider) ID() string          { return p.cfg.Name }
func (p *Provider) Match(m string) bool { return strings.HasPrefix(m, p.cfg.ModelPrefix) }

// source — nguồn của block image/document: data URI → base64, còn lại → url.
func source(u string) (map[string]any, error) {
	rest, isData := strings.CutPrefix(u, "data:")
	if !isData {
		return map[string]any{"type": "url", "url": u}, nil
	}
	mediaType, data, ok := strings.Cut(rest, ";base64,")
	if !ok {
		return nil, fmt.Errorf("data URI phải dạng data:<media-type>;base64,<dữ liệu>")
	}
	mediaType, _, _ = strings.Cut(mediaType, ";") // bỏ tham số (charset, name…): Anthropic chỉ nhận media type trần
	return map[string]any{"type": "base64", "media_type": mediaType, "data": data}, nil
}

// toBlocks — part của user → content block Messages API.
func toBlocks(parts []provider.Part) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		if p.Type == provider.PartText {
			if p.Text != "" { // Messages API trả 400 với block text rỗng (client gửi ảnh không kèm chữ)
				out = append(out, map[string]any{"type": "text", "text": p.Text})
			}
			continue
		}
		src, err := source(p.URL)
		if err != nil {
			return nil, err
		}
		blockType := "image"
		if p.Type == provider.PartFile {
			blockType = "document"
		}
		out = append(out, map[string]any{"type": blockType, "source": src})
	}
	return out, nil
}

// toPayload — dịch request OpenAI sang body messages của Anthropic: system
// messages gộp thành "system", còn lại giữ vai; user gửi dạng mảng block
// (text/image/document), assistant gửi string.
func (p *Provider) toPayload(req provider.ChatRequest) (map[string]any, error) {
	if req.ReasoningEffort != "" {
		// Messages API điều khiển suy nghĩ bằng cơ chế khác; chưa dịch thì
		// báo lỗi rõ thay vì lặng lẽ bỏ qua.
		return nil, fmt.Errorf("reasoning_effort chưa hỗ trợ cho backend Claude — bỏ field này")
	}
	var system []string
	var messages []map[string]any
	for i, m := range req.Messages {
		var err error
		switch m.Role {
		case "system", "developer":
			var text string
			if text, err = m.ContentText(); err == nil {
				system = append(system, text)
			}
		case "assistant":
			var text string
			if text, err = m.ContentText(); err == nil {
				messages = append(messages, map[string]any{"role": m.Role, "content": text})
			}
		case "user":
			var parts []provider.Part
			if parts, err = m.Parts(); err == nil {
				var blocks []map[string]any
				if blocks, err = toBlocks(parts); err == nil {
					messages = append(messages, map[string]any{"role": m.Role, "content": blocks})
				}
			}
		default:
			err = fmt.Errorf("role %q không dùng được với backend Claude", m.Role)
		}
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
	}
	maxTokens := p.cfg.MaxTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
	payload := map[string]any{
		"model":      req.Model,
		"messages":   messages,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	if len(system) > 0 {
		payload["system"] = strings.Join(system, "\n\n")
	}
	return payload, nil
}

// decodeEvent — dịch một sự kiện SSE messages của Anthropic về StreamEvent.
func decodeEvent(data []byte) (provider.StreamEvent, error) {
	var ev struct {
		Type  string `json:"type"`
		Delta struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		// Usage của Anthropic dùng input_tokens/output_tokens — key khác
		// OpenAI — nên parse riêng rồi đổi sang Usage.
		Message struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return provider.StreamEvent{}, fmt.Errorf("parse sự kiện claude: %w", err)
	}
	switch ev.Type {
	case "content_block_delta":
		if ev.Delta.Type == "text_delta" {
			return provider.StreamEvent{Text: ev.Delta.Text}, nil
		}
	case "message_start":
		u := ev.Message.Usage
		return provider.StreamEvent{Usage: &provider.Usage{PromptTokens: u.InputTokens, TotalTokens: u.InputTokens}}, nil
	case "message_delta":
		return provider.StreamEvent{Usage: &provider.Usage{CompletionTokens: ev.Usage.OutputTokens}}, nil
	case "message_stop":
		return provider.StreamEvent{Done: true}, nil
	case "error":
		return provider.StreamEvent{}, fmt.Errorf("backend claude lỗi: %s", ev.Error.Message)
	}
	return provider.StreamEvent{}, nil
}

func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	cred, err := p.cred.Get(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	p.setHeaders(req, cred)
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("claude /models HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

func (p *Provider) StreamChat(ctx context.Context, req provider.ChatRequest, emit func(provider.StreamEvent) error) error {
	payload, err := p.toPayload(req) // kiểm request trước: request sai không kích refresh token
	if err != nil {
		return &provider.RequestError{Err: err}
	}
	cred, err := p.cred.Get(ctx)
	if err != nil {
		return &provider.AccountError{Err: err}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/v1/messages?beta=true", bytes.NewReader(body))
	if err != nil {
		return err
	}
	p.setHeaders(httpReq, cred)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	return provider.StreamSSE(p.http, httpReq, "claude /messages", decodeEvent, emit)
}

// setHeaders — nhận dạng: OAuth token của Claude Code, KHÔNG phải x-api-key.
func (p *Provider) setHeaders(req *http.Request, cred tokenstore.Credentials) {
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("anthropic-version", p.cfg.APIVersion)
	req.Header.Set("anthropic-beta", p.cfg.Beta)
	req.Header.Set("User-Agent", p.cfg.UserAgent)
}
