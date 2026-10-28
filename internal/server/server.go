// Package server — mặt HTTP: hai route của mặt OpenAI (/v1/models,
// /v1/chat/completions) + /healthz + /status (số đếm từng tài khoản). Phần
// stream: upstream luôn stream, client muốn non-stream thì gom buffer thành
// một completion. Chọn tài khoản + failover nằm ở provider.Router.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/luannt2002/subllm/internal/provider"
)

// maxChatBody — trần body POST /v1/chat/completions: giới hạn an toàn bộ
// nhớ (body đọc hết vào RAM), dư cho vài ảnh/PDF base64; không phải trần của
// backend. Biến (không phải hằng) để test hạ xuống.
var maxChatBody int64 = 64 << 20

// Server — http.Handler phục vụ mặt OpenAI trên các provider của router.
type Server struct {
	router   *provider.Router
	apiKey   string
	reqCount atomic.Int64
}

// New — apiKey rỗng = không kiểm key (hợp với localhost).
func New(router *provider.Router, apiKey string) *Server {
	return &Server{router: router, apiKey: apiKey}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Trình duyệt (có Origin) hay tên miền lạ trỏ về 127.0.0.1 (DNS
	// rebinding → Host lạ): chặn, kẻo một trang web tiêu được gói khi chưa
	// đặt API key. Client cùng máy (curl, SDK) không gửi Origin.
	if r.Header.Get("Origin") != "" || !loopbackHost(r.Host) {
		writeErr(w, http.StatusForbidden, "subllm chỉ nhận request từ chương trình cùng máy (không Origin, Host là loopback)", "forbidden")
		return
	}
	switch r.URL.Path {
	case "/healthz":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	case "/status":
		s.handleStatus(w, r)
	case "/v1/models":
		s.handleModels(w, r)
	case "/v1/chat/completions":
		s.handleChat(w, r)
	default:
		writeErr(w, http.StatusNotFound, "route "+r.URL.Path+" không tồn tại", "not_found")
	}
}

// loopbackHost — Host header là 127.0.0.1 / ::1 / localhost (có hoặc không port).
func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

// authed — kiểm api-key nếu có đặt; localhost-only thì để trống là đủ.
func (s *Server) authed(w http.ResponseWriter, r *http.Request) bool {
	if s.apiKey == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == s.apiKey {
		return true
	}
	writeErr(w, http.StatusUnauthorized, "api key sai hoặc thiếu", "auth")
	return false
}

func writeErr(w http.ResponseWriter, status int, message, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errJSON(message, typ))
}

// writeRouteErr — lỗi trước khi stream bắt đầu → mã HTTP thật: 400 request
// sai (không dịch được, hoặc upstream trả 400), 404 không có provider, 429 mọi tài khoản đang nghỉ / dính
// hạn mức (kèm Retry-After để client tự lùi), còn lại 502.
func writeRouteErr(w http.ResponseWriter, err error) {
	if provider.ClientFault(err) {
		writeErr(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	var nf *provider.NotFoundError
	if errors.As(err, &nf) {
		writeErr(w, http.StatusNotFound, err.Error(), "model_not_found")
		return
	}
	var up *provider.UpstreamError
	if errors.As(err, &up) && up.Status == http.StatusTooManyRequests {
		if up.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(up.RetryAfter.Seconds()))))
		}
		writeErr(w, http.StatusTooManyRequests, err.Error(), "rate_limit_error")
		return
	}
	writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
}

// handleStatus — số đếm từng tài khoản; có api-key thì đòi key vì lộ tên
// tài khoản.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"providers": s.router.Status()})
}

// handleModels — catalog gộp; đòi key như /status (owned_by lộ tên tài khoản,
// và mỗi lần gọi đều chạm credential upstream).
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	data := []map[string]any{}
	for _, p := range s.router.Providers() {
		ids, err := p.ListModels(r.Context())
		if err != nil {
			log.Printf("models %s: %v", p.ID(), err)
			continue // backend một con chết vẫn trả phần còn lại
		}
		for _, id := range ids {
			data = append(data, map[string]any{"id": id, "object": "model", "owned_by": p.ID()})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "dùng POST", "invalid_request_error")
		return
	}
	if !s.authed(w, r) {
		return
	}
	var req provider.ChatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBody)).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body quá lớn (tối đa %d MiB)", maxChatBody>>20), "invalid_request_error")
			return
		}
		writeErr(w, http.StatusBadRequest, "body JSON lỗi: "+err.Error(), "invalid_request_error")
		return
	}
	if req.Model == "" || len(req.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "thiếu model hoặc messages", "invalid_request_error")
		return
	}
	plan, err := s.router.Plan(req)
	if err != nil {
		writeRouteErr(w, err)
		return
	}

	reqID := fmt.Sprintf("subllm-%d", s.reqCount.Add(1))
	if req.Stream {
		s.serveStream(w, r, plan, req, reqID)
	} else {
		s.serveBuffered(w, r, plan, req, reqID)
	}
}

func (s *Server) serveBuffered(w http.ResponseWriter, r *http.Request, plan []provider.Provider, req provider.ChatRequest, reqID string) {
	var text strings.Builder
	var usage provider.Usage
	finish := "stop" // backend báo bị cắt thì đổi theo ev.Finish
	err := s.router.Stream(r.Context(), plan, req, func(ev provider.StreamEvent) error {
		if ev.Usage != nil {
			usage = usage.Add(*ev.Usage)
		}
		if ev.Finish != "" {
			finish = ev.Finish
		}
		text.WriteString(ev.Text)
		return nil
	})
	if err != nil {
		log.Printf("chat %s %s: %v", reqID, req.Model, err)
		writeRouteErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(completion{
		ID: reqID, Object: "chat.completion", Created: time.Now().Unix(), Model: req.Model,
		Choices: []choiceOut{{Index: 0, Message: messageOut{Role: "assistant", Content: text.String()}, FinishReason: finish}},
		Usage:   usage,
	})
}

func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, plan []provider.Provider, req provider.ChatRequest, reqID string) {
	start := time.Now()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "server không hỗ trợ stream", "internal_error")
		return
	}
	// Header SSE ghi ở chunk đầu tiên: lỗi trước đó (mọi tài khoản 429, hết
	// failover…) còn trả được mã HTTP thật thay vì 200 + chunk lỗi.
	started := false
	startSSE := func() {
		started = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
	}

	var usage provider.Usage
	finish := "stop" // backend báo bị cắt thì đổi theo ev.Finish
	err := s.router.Stream(r.Context(), plan, req, func(ev provider.StreamEvent) error {
		if ev.Usage != nil {
			usage = usage.Add(*ev.Usage)
		}
		if ev.Finish != "" {
			finish = ev.Finish
		}
		if ev.Text == "" {
			return nil
		}
		if !started {
			startSSE()
			writeChunk(w, reqID, req.Model, deltaBody{Role: "assistant"}, "")
		}
		writeChunk(w, reqID, req.Model, deltaBody{Content: ev.Text}, "")
		flusher.Flush()
		return nil
	})
	if err != nil && !started {
		log.Printf("chat %s %s: %v", reqID, req.Model, err)
		writeRouteErr(w, err)
		return
	}
	if err != nil {
		// Đã gửi headers nên không đổi status được nữa: báo lỗi qua chunk
		// cuối với finish_reason="error" + ghi log server.
		log.Printf("chat %s %s: %v", reqID, req.Model, err)
		finish = "error"
	}
	if !started { // upstream xong mà không có chữ nào: vẫn trả stream hợp lệ
		startSSE()
	}
	writeChunk(w, reqID, req.Model, deltaBody{}, finish)
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
	log.Printf("chat %s %s xong sau %s (usage %+v)", reqID, req.Model, time.Since(start).Round(time.Millisecond), usage)
}

// writeChunk — ghi một sự kiện "data:"; finish rỗng = chưa kết thúc
// (finish_reason null).
func writeChunk(w http.ResponseWriter, reqID, model string, delta deltaBody, finish string) {
	ch := chunk{
		ID: reqID, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
		Choices: []chunkChoice{{Index: 0, Delta: delta}},
	}
	if finish != "" {
		ch.Choices[0].FinishReason = &finish
	}
	line, err := json.Marshal(ch)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
}
