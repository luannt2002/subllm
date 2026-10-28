// Package codex — strategy backend Codex (gói ChatGPT), nói Responses API.
// Luồng đã verify (tham khảo hotchpotch/openai-api-server-via-codex):
//   - token: file auth của Codex CLI → object "tokens"; refresh dạng
//     form-urlencoded với client_id của Codex CLI;
//   - gọi: POST {base}/responses — BẮT BUỘC stream:true và store:false, kèm
//     header chatgpt-account-id;
//   - quirk: backend từ chối max_output_tokens → không bao giờ gửi nó.
//
// Endpoint, client ID, phiên bản… không nằm trong code: config.go đọc từ env
// (mẫu ở .env.example); nhiều tài khoản = nhiều provider cùng kiểu codex.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"subllm/internal/provider"
	"subllm/internal/tokenstore"
)

// Config — cấu hình một provider kiểu codex; config.go đọc từng field từ
// env.
type Config struct {
	Name          string // tên provider (ID, field "provider" trong body)
	CredFile      string // file auth của Codex CLI
	ModelPrefix   string // model mang tiền tố này route về Codex
	BaseURL       string // gốc backend, ghép thêm /responses, /models
	TokenURL      string // endpoint refresh OAuth
	ClientID      string // client ID công khai của Codex CLI
	ClientVersion string // backend /models đòi query client_version
	Originator    string // header originator
	Beta          string // header OpenAI-Beta
	UserAgent     string // header User-Agent
	// Timeout — trần một request upstream, tính cả lúc đọc stream.
	Timeout        time.Duration
	RefreshMargin  time.Duration // refresh khi token còn hạn dưới mức này
	RefreshTimeout time.Duration // trần một lượt refresh
}

// Provider — strategy Codex, cài provider.Provider.
type Provider struct {
	cfg  Config
	cred *tokenstore.Store
	http *http.Client
}

// New — strategy Codex theo cfg.
func New(cfg Config) *Provider {
	c := tokenstore.New(cfg.CredFile, "tokens", cfg.RefreshMargin, cfg.RefreshTimeout)
	c.AccessKey = "access_token"
	c.RefreshKey = "refresh_token"
	c.AccountKey = "account_id"
	c.LastRefreshKey = "last_refresh"
	c.RefreshURL = cfg.TokenURL
	c.ClientID = cfg.ClientID
	// JSONBody=false: refresh dạng form-urlencoded.
	return &Provider{cfg: cfg, cred: c, http: &http.Client{Timeout: cfg.Timeout}}
}

func (p *Provider) ID() string          { return p.cfg.Name }
func (p *Provider) Match(m string) bool { return strings.HasPrefix(m, p.cfg.ModelPrefix) }

// toItem — một tin nhắn OpenAI → một item Responses API.
func toItem(role string, content []map[string]any) map[string]any {
	return map[string]any{"type": "message", "role": role, "content": content}
}

// toContent — part của user → content Responses API (ảnh/file đã verify
// live với data URI: backend đọc được cả hai).
func toContent(parts []provider.Part) []map[string]any {
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case provider.PartImage:
			out = append(out, map[string]any{"type": "input_image", "image_url": p.URL})
		case provider.PartFile:
			out = append(out, map[string]any{"type": "input_file", "filename": p.Filename, "file_data": p.URL})
		default:
			out = append(out, map[string]any{"type": "input_text", "text": p.Text})
		}
	}
	return out
}

// toPayload — dịch request OpenAI sang body Responses API. System messages
// gộp vào "instructions"; reasoning_effort → "reasoning": {"effort": …};
// body giữ đúng những field backend nhận.
func toPayload(req provider.ChatRequest) (map[string]any, error) {
	var instructions []string
	var input []map[string]any
	for i, m := range req.Messages {
		var err error
		switch m.Role {
		case "system", "developer":
			var text string
			if text, err = m.ContentText(); err == nil {
				instructions = append(instructions, text)
			}
		case "assistant":
			var text string
			if text, err = m.ContentText(); err == nil {
				input = append(input, toItem(m.Role, []map[string]any{{"type": "output_text", "text": text}}))
			}
		case "user":
			var parts []provider.Part
			if parts, err = m.Parts(); err == nil {
				input = append(input, toItem(m.Role, toContent(parts)))
			}
		default:
			err = fmt.Errorf("role %q không dùng được với backend Codex", m.Role)
		}
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
	}
	payload := map[string]any{
		"model":        req.Model,
		"input":        input,
		"instructions": strings.Join(instructions, "\n\n"),
		"stream":       true, // backend BẮT BUỘC stream
		"store":        false,
	}
	if req.ReasoningEffort != "" {
		// Mức hợp lệ tuỳ model (xem /models) — backend kiểm, sai thì 400.
		payload["reasoning"] = map[string]any{"effort": req.ReasoningEffort}
	}
	return payload, nil
}

// decodeEvent — dịch một sự kiện SSE của Responses API về StreamEvent
// trung tính. Chỉ quan tâm text delta, usage, kết thúc.
func decodeEvent(data []byte) (provider.StreamEvent, error) {
	var ev struct {
		Type     string `json:"type"`
		Delta    string `json:"delta"`
		Response struct {
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				TotalTokens  int `json:"total_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return provider.StreamEvent{}, fmt.Errorf("parse sự kiện codex: %w", err)
	}
	switch {
	case ev.Type == "response.output_text.delta":
		return provider.StreamEvent{Text: ev.Delta}, nil
	case ev.Type == "response.completed":
		out := provider.StreamEvent{Done: true}
		if u := ev.Response.Usage; u != nil {
			out.Usage = &provider.Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
		}
		return out, nil
	case ev.Type == "response.failed" || strings.HasPrefix(ev.Type, "error"):
		return provider.StreamEvent{}, fmt.Errorf("backend codex lỗi: %s", provider.Truncate(data, provider.ErrSnippet))
	}
	return provider.StreamEvent{}, nil // sự kiện khác (created, in_progress…) bỏ qua
}

func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	cred, err := p.cred.Get(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.cfg.BaseURL+"/models?client_version="+url.QueryEscape(p.cfg.ClientVersion), nil)
	if err != nil {
		return nil, err
	}
	p.setHeaders(req, cred, false)
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codex /models HTTP %d", resp.StatusCode)
	}
	var out struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range out.Models {
		if m.Visibility == "list" && m.Slug != "" {
			ids = append(ids, m.Slug)
		}
	}
	return ids, nil
}

func (p *Provider) StreamChat(ctx context.Context, req provider.ChatRequest, emit func(provider.StreamEvent) error) error {
	payload, err := toPayload(req) // kiểm request trước: request sai không kích refresh token
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return err
	}
	p.setHeaders(httpReq, cred, true)
	return provider.StreamSSE(p.http, httpReq, "codex /responses", decodeEvent, emit)
}

// setHeaders — nhận dạng trước backend Codex: token + account + originator.
func (p *Provider) setHeaders(req *http.Request, cred tokenstore.Credentials, stream bool) {
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("originator", p.cfg.Originator)
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	if cred.AccountID != "" {
		req.Header.Set("chatgpt-account-id", cred.AccountID)
	}
	if stream {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("OpenAI-Beta", p.cfg.Beta)
	}
}
