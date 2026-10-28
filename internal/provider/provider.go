// Package provider — Strategy pattern: mỗi backend subscription là một
// strategy cài interface Provider. Thêm backend mới = viết thêm một package
// con cài interface rồi thêm một dòng vào map adapters ở config.go, không đụng
// phần còn lại.
package provider

import (
	"context"
	"fmt"
	"strings"
)

// StreamEvent — một nốt phát ra từ upstream, trung tính giữa các backend:
// text là mẩu chữ, Usage cộng dồn, Done báo kết thúc.
type StreamEvent struct {
	Text  string
	Usage *Usage
	Done  bool
}

// Provider — một backend subscription (codex, claude…).
type Provider interface {
	// ID — tên ngắn dùng trong log.
	ID() string
	// Match — model nào thuộc strategy này (route theo tiền tố model khai
	// trong env của provider).
	Match(model string) bool
	// ListModels — slug/id model của gói, để phục vụ GET /v1/models.
	ListModels(ctx context.Context) ([]string, error)
	// StreamChat — dịch request OpenAI sang ngôn ngữ backend, gọi upstream,
	// phát từng StreamEvent về qua emit. Backend nào bắt buộc stream thì
	// tự luôn-stream rồi buffer bên trong.
	StreamChat(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) error
}

// Registry — danh sách strategy, thứ tự quyết định khi hai strategy cùng
// khớp một model.
type Registry []Provider

// Route — chọn strategy cho model theo tiền tố.
func (r Registry) Route(model string) (Provider, error) {
	for _, p := range r {
		if p.Match(model) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("không có backend nào nhận model %q (subllm phục vụ: %s)", model, r.ids("-*"))
}

// ByID — tìm strategy đích danh theo tên ("codex", "claude"…); dùng khi
// client đặt field "provider" trong body để thắng việc route theo tiền tố.
func (r Registry) ByID(id string) (Provider, error) {
	for _, p := range r {
		if strings.EqualFold(p.ID(), id) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("không có provider %q (subllm có: %s)", id, r.ids(""))
}

// ids — liệt kê ID các strategy (kèm hậu tố) cho thông báo lỗi.
func (r Registry) ids(suffix string) string {
	out := make([]string, 0, len(r))
	for _, p := range r {
		out = append(out, p.ID()+suffix)
	}
	return strings.Join(out, ", ")
}
