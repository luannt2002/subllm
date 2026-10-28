package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"subllm/internal/provider"
)

// fake — provider giả: err != nil thì lỗi trước khi phát gì; text thì phát.
type fake struct {
	id   string
	text string
	err  error
}

func (f *fake) ID() string                                   { return f.id }
func (f *fake) Match(m string) bool                          { return strings.HasPrefix(m, "m-") }
func (f *fake) ListModels(context.Context) ([]string, error) { return []string{f.id + "-model"}, nil }
func (f *fake) StreamChat(_ context.Context, _ provider.ChatRequest, emit func(provider.StreamEvent) error) error {
	if f.err != nil {
		return f.err
	}
	emit(provider.StreamEvent{Text: f.text})
	return nil
}

func call(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestChatFailoverAndStatus(t *testing.T) {
	limited := &provider.UpstreamError{Status: 429, RetryAfter: 90 * time.Second, Msg: "claude /messages HTTP 429: hết lượt"}
	s := New(provider.NewRouter(provider.Registry{&fake{id: "acc1", err: limited}, &fake{id: "acc2", text: "chào"}}, time.Minute), "")

	rec := call(t, s, "POST", "/v1/chat/completions", `{"model":"m-x","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"content":"chào"`) {
		t.Fatalf("acc1 429 → acc2 phải trả lời: %d %s", rec.Code, rec.Body)
	}
	// /status: acc1 đang nghỉ + 1 lần failover, acc2 có 1 lần OK.
	rec = call(t, s, "GET", "/status", "")
	var st struct{ Providers []provider.AccountStatus }
	json.Unmarshal(rec.Body.Bytes(), &st)
	if len(st.Providers) != 2 || st.Providers[0].Failovers != 1 || st.Providers[0].CoolingUntil == nil || st.Providers[1].OK != 1 ||
		strings.Contains(rec.Body.String(), "0001-01-01") {
		t.Fatalf("/status sai: %s", rec.Body)
	}
}

func TestChatErrorStatus(t *testing.T) {
	limited := &provider.UpstreamError{Status: 429, RetryAfter: 90 * time.Second, Msg: "HTTP 429"}
	for name, tc := range map[string]struct {
		err       error
		stream    bool
		wantCode  int
		wantType  string
		wantRetry string
	}{
		"429 non-stream":                       {err: limited, wantCode: 429, wantType: "rate_limit_error", wantRetry: "90"},
		"429 stream":                           {err: limited, stream: true, wantCode: 429, wantType: "rate_limit_error", wantRetry: "90"},
		"lỗi khác non-stream":                  {err: errors.New("hỏng"), wantCode: 502, wantType: "upstream_error"},
		"request sai (role lạ)":                {err: &provider.RequestError{Err: errors.New("role lạ")}, wantCode: 400, wantType: "invalid_request_error"},
		"upstream 400 (vd mức effort sai)":     {err: &provider.UpstreamError{Status: 400, Msg: "codex /responses HTTP 400: effort"}, wantCode: 400, wantType: "invalid_request_error"},
		"429 không Retry-After → lấy cooldown": {err: &provider.UpstreamError{Status: 429, Msg: "HTTP 429"}, wantCode: 429, wantType: "rate_limit_error", wantRetry: "60"},
		// header SSE ghi muộn: lỗi trước chữ đầu tiên vẫn trả mã HTTP thật
		"lỗi khác stream": {err: &provider.AccountError{Err: errors.New("thiếu file")}, stream: true, wantCode: 502, wantType: "upstream_error"},
	} {
		s := New(provider.NewRouter(provider.Registry{&fake{id: "acc1", err: tc.err}}, time.Minute), "")
		body := `{"model":"m-x","messages":[{"role":"user","content":"hi"}]`
		if tc.stream {
			body += `,"stream":true`
		}
		rec := call(t, s, "POST", "/v1/chat/completions", body+"}")
		if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), `"type":"`+tc.wantType+`"`) ||
			!strings.Contains(rec.Body.String(), "acc1: ") || rec.Header().Get("Retry-After") != tc.wantRetry {
			t.Fatalf("%s: %d Retry-After=%q %s", name, rec.Code, rec.Header().Get("Retry-After"), rec.Body)
		}
	}
	// Mọi tài khoản đang nghỉ → 429 ngay (không gọi upstream), kèm Retry-After.
	s := New(provider.NewRouter(provider.Registry{&fake{id: "acc1", err: limited}}, time.Minute), "")
	call(t, s, "POST", "/v1/chat/completions", `{"model":"m-x","messages":[{"role":"user","content":"hi"}]}`)
	rec := call(t, s, "POST", "/v1/chat/completions", `{"model":"m-x","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "đang nghỉ") {
		t.Fatalf("tất cả đang nghỉ phải 429 + Retry-After: %d %s", rec.Code, rec.Body)
	}
	// Không có provider → 404.
	rec = call(t, s, "POST", "/v1/chat/completions", `{"model":"khac-x","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "model_not_found") {
		t.Fatalf("model lạ phải 404: %d %s", rec.Code, rec.Body)
	}
}

func TestStreamOK(t *testing.T) {
	s := New(provider.NewRouter(provider.Registry{&fake{id: "acc1", text: "chào"}}, time.Minute), "")
	rec := call(t, s, "POST", "/v1/chat/completions", `{"model":"m-x","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	b := rec.Body.String()
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" ||
		!strings.Contains(b, `"delta":{"role":"assistant"}`) || !strings.Contains(b, `"delta":{"content":"chào"}`) ||
		!strings.Contains(b, `"finish_reason":"stop"`) || !strings.HasSuffix(b, "data: [DONE]\n\n") {
		t.Fatalf("stream sai: %d %s", rec.Code, b)
	}
}

func TestStatusNeedsKey(t *testing.T) {
	s := New(provider.NewRouter(provider.Registry{&fake{id: "acc1"}}, time.Minute), "k")
	if rec := call(t, s, "GET", "/status", ""); rec.Code != 401 {
		t.Fatalf("/status lộ tên tài khoản, có api key thì phải đòi: %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/status", nil)
	req.Header.Set("Authorization", "Bearer k")
	s.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"acc1"`) {
		t.Fatalf("đúng key phải xem được /status: %d %s", rec.Code, rec.Body)
	}
}

// /v1/models cũng lộ tên tài khoản (owned_by) và gọi upstream bằng
// credential: có api key thì phải đòi như /status.
func TestModelsNeedsKey(t *testing.T) {
	s := New(provider.NewRouter(provider.Registry{&fake{id: "acc1"}}, time.Minute), "k")
	if rec := call(t, s, "GET", "/v1/models", ""); rec.Code != 401 {
		t.Fatalf("/v1/models có api key thì phải đòi: %d %s", rec.Code, rec.Body)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer k")
	s.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"owned_by":"acc1"`) {
		t.Fatalf("đúng key phải xem được /v1/models: %d %s", rec.Code, rec.Body)
	}
}

// spy — provider giả ghi lại request nhận được.
type spy struct {
	fake
	got provider.ChatRequest
}

func (s *spy) StreamChat(ctx context.Context, req provider.ChatRequest, emit func(provider.StreamEvent) error) error {
	s.got = req
	return s.fake.StreamChat(ctx, req, emit)
}

// reasoning_effort trong body tới được provider (non-stream lẫn stream).
func TestReasoningEffortReachesProvider(t *testing.T) {
	for _, stream := range []string{"false", "true"} {
		sp := &spy{fake: fake{id: "acc1", text: "ok"}}
		s := New(provider.NewRouter(provider.Registry{sp}, time.Minute), "")
		rec := call(t, s, "POST", "/v1/chat/completions", `{"model":"m-x","stream":`+stream+`,"reasoning_effort":"e-y","messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != 200 || sp.got.ReasoningEffort != "e-y" {
			t.Fatalf("stream=%s: provider phải nhận reasoning_effort=e-y, nhận %q (HTTP %d)", stream, sp.got.ReasoningEffort, rec.Code)
		}
	}
}
