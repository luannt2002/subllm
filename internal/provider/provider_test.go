package provider

import (
	"encoding/json"
	"strings"
	"testing"
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
	if parts, _ := (ChatMessage{Content: json.RawMessage(`"chỉ chữ"`)}).Parts(); len(parts) != 1 || parts[0] != (Part{Type: PartText, Text: "chỉ chữ"}) {
		t.Fatalf("content string phải thành 1 part text: %+v", parts)
	}
	// Kiểu chưa hỗ trợ / thiếu dữ liệu: lỗi rõ, không bỏ lặng lẽ.
	for _, bad := range []string{
		`[{"type":"input_audio","input_audio":{"data":"x","format":"wav"}}]`,
		`[{"type":"image_url","image_url":{}}]`,
		`[{"type":"file","file":{"file_id":"file-1"}}]`,
	} {
		if _, err := (ChatMessage{Content: json.RawMessage(bad)}).Parts(); err == nil {
			t.Fatalf("phải lỗi: %s", bad)
		}
	}
}
