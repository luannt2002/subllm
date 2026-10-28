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
	m = ChatMessage{Content: json.RawMessage(`[{"type":"text","text":"a"},{"type":"image_url"},{"type":"text","text":"b"}]`)}
	if s, _ := m.ContentText(); s != "ab" {
		t.Fatalf("content mảng part sai: %q", s)
	}
}
