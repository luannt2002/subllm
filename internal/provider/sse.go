package provider

// sse.go — đọc luồng Server-Sent Events (SSE) từ upstream. Cả hai backend
// Codex và Claude đều trả SSE, mỗi sự kiện là một dòng "data: <json>";
// phần "event:" chỉ là nhãn, JSON tự mang trường type rồi.

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Giới hạn an toàn bộ nhớ — không phải cấu hình triển khai.
const (
	maxEventBytes = 4 << 20 // một sự kiện SSE dài nhất nhận (reasoning có thể rất dài)
	maxErrorBody  = 1 << 20 // đọc tối đa ngần này body của phản hồi lỗi
	// ErrSnippet — số byte body đưa vào thông báo lỗi.
	ErrSnippet = 300
)

// ScanSSE — đọc từng sự kiện từ r, gọi handle với mỗi payload "data:".
// Dừng khi stream kết thúc, handle trả lỗi, hoặc gặp "[DONE]" (không gọi
// handle cho nó). payload rỗng/là comment bị bỏ qua.
func ScanSSE(r io.Reader, handle func(data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventBytes)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			return nil
		}
		if err := handle([]byte(data)); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("đọc stream: %w", err)
	}
	return nil
}

// StreamSSE — gửi req, đòi HTTP 200, rồi đọc luồng SSE: mỗi payload qua
// decode thành StreamEvent rồi emit. Khác 200 → *UpstreamError (Router dựa
// vào Status/RetryAfter để failover); label (vd "codex /responses") đứng đầu
// thông báo lỗi.
func StreamSSE(client *http.Client, req *http.Request, label string,
	decode func(data []byte) (StreamEvent, error), emit func(StreamEvent) error) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		wait := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if wait == 0 && resp.StatusCode == http.StatusTooManyRequests {
			wait = retryAfterFromBody(raw)
		}
		return &UpstreamError{
			Status:     resp.StatusCode,
			RetryAfter: wait,
			Msg:        fmt.Sprintf("%s HTTP %d: %s", label, resp.StatusCode, Truncate(raw, ErrSnippet)),
		}
	}
	return ScanSSE(resp.Body, func(data []byte) error {
		ev, err := decode(data)
		if err != nil {
			return err
		}
		return emit(ev)
	})
}

// Truncate — cắt chuỗi byte dài để đưa vào thông báo lỗi.
func Truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
