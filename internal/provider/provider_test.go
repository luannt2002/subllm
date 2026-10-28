package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScanSSE(t *testing.T) {
	stream := strings.Join([]string{
		"event: a",
		`data: {"n":1}`,
		"",
		": comment bỏ qua",
		`data: {"n":2}`,
		"",
		"data: [DONE]",
		`data: {"n":3}`, // sau DONE không được đọc nữa
	}, "\n")
	var got []int
	err := ScanSSE(strings.NewReader(stream), func(data []byte) error {
		var v struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		got = append(got, v.N)
		return nil
	})
	if err != nil {
		t.Fatalf("ScanSSE lỗi: %v", err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("ScanSSE nhận %v, muốn [1 2]", got)
	}
}

func TestContentText(t *testing.T) {
	m := ChatMessage{Content: json.RawMessage(`"đơn giản"`)}
	if s, _ := m.ContentText(); s != "đơn giản" {
		t.Fatalf("content string sai: %q", s)
	}
	m = ChatMessage{Content: json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)}
	if s, _ := m.ContentText(); s != "ab" {
		t.Fatalf("content mảng part sai: %q", s)
	}
	// Vai chỉ nhận chữ (system/assistant) gặp ảnh: phải lỗi, không lặng lẽ bỏ ảnh.
	m = ChatMessage{Content: json.RawMessage(`[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"https://x/a.png"}}]`)}
	if _, err := m.ContentText(); err == nil {
		t.Fatal("ContentText gặp ảnh phải lỗi")
	}
}

// Một lượt trộn nhiều text + nhiều ảnh + file, giữ đúng thứ tự.
func TestContentParts(t *testing.T) {
	m := ChatMessage{Content: json.RawMessage(`[
		{"type":"text","text":"câu 1"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}},
		{"type":"image_url","image_url":{"url":"https://x/b.jpg"}},
		{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,BBB"}},
		{"type":"text","text":"câu 2"}]`)}
	parts, err := m.Parts()
	if err != nil {
		t.Fatal(err)
	}
	want := []Part{
		{Type: PartText, Text: "câu 1"},
		{Type: PartImage, URL: "data:image/png;base64,AAA"},
		{Type: PartImage, URL: "https://x/b.jpg"},
		{Type: PartFile, URL: "data:application/pdf;base64,BBB", Filename: "a.pdf"},
		{Type: PartText, Text: "câu 2"},
	}
	if len(parts) != len(want) {
		t.Fatalf("nhận %d part, muốn %d: %+v", len(parts), len(want), parts)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("part[%d] = %+v, muốn %+v", i, parts[i], want[i])
		}
	}
	// part chỉ có chữ mà thiếu "type": vẫn là text (client cũ gửi vậy).
	if parts, err := (ChatMessage{Content: json.RawMessage(`[{"text":"không type"}]`)}).Parts(); err != nil || len(parts) != 1 || parts[0] != (Part{Type: PartText, Text: "không type"}) {
		t.Fatalf("part chữ thiếu type phải là text: %+v %v", parts, err)
	}
	if parts, _ := (ChatMessage{Content: json.RawMessage(`"chỉ chữ"`)}).Parts(); len(parts) != 1 || parts[0] != (Part{Type: PartText, Text: "chỉ chữ"}) {
		t.Fatalf("content string phải thành 1 part text: %+v", parts)
	}
	// Kiểu chưa hỗ trợ / thiếu dữ liệu: lỗi rõ, không bỏ lặng lẽ.
	for _, bad := range []string{
		`[{"type":"input_audio","input_audio":{"data":"x","format":"wav"}}]`,
		`[{"type":"image_url","image_url":{}}]`,
		`[{"type":"file","file":{"file_id":"file-1"}}]`,
		// thiếu "type" mà mang ảnh/file: trước đây thành chữ rỗng, ảnh rơi lặng lẽ
		`[{"image_url":{"url":"data:image/png;base64,AAA"}}]`,
		`[{"file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,BBB"}}]`,
	} {
		if _, err := (ChatMessage{Content: json.RawMessage(bad)}).Parts(); err == nil {
			t.Fatalf("phải lỗi: %s", bad)
		}
	}
}

// StreamSSE với HTTP thật: lỗi upstream phải thành UpstreamError đúng mã và
// thời gian nghỉ — router dựa vào đó để failover và cho tài khoản nghỉ.
// Codex báo giờ reset hạn mức trong body 429 (error.resets_in_seconds),
// không qua header Retry-After.
func TestStreamSSEUpstreamError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header string
		body   string
		want   time.Duration
	}{
		{"429 + Retry-After", 429, "120", `{"error":{"message":"x"}}`, 2 * time.Minute},
		{"429 chỉ có resets_in_seconds", 429, "", `{"error":{"type":"usage_limit_reached","resets_at":1,"limit_window_minutes":300,"resets_in_seconds":4313}}`, 4313 * time.Second},
		{"503", 503, "", `{"error":{"resets_in_seconds":9}}`, 0},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.header != "" {
				w.Header().Set("Retry-After", tc.header)
			}
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}))
		req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
		err := StreamSSE(srv.Client(), req, "t", nil, nil)
		srv.Close()
		var up *UpstreamError
		if !errors.As(err, &up) || up.Status != tc.status || up.RetryAfter != tc.want {
			t.Fatalf("%s: muốn UpstreamError{%d, %v}, nhận %T %+v", tc.name, tc.status, tc.want, err, err)
		}
	}
}
