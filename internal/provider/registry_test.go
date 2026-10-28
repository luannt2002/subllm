package provider_test

// Test route dùng strategy THẬT (codex, claude) để kiểm luôn Match của
// chúng; package _test riêng nên import codex/claude không bị vòng import.

import (
	"strings"
	"testing"

	"subllm/internal/provider"
	"subllm/internal/provider/claude"
	"subllm/internal/provider/codex"
)

func registry() provider.Registry {
	return provider.Registry{
		codex.New(codex.Config{Name: "codex", ModelPrefix: "pa-"}),
		claude.New(claude.Config{Name: "claude", ModelPrefix: "pb-"}),
	}
}

func TestRoute(t *testing.T) {
	r := registry()
	if p, err := r.Route("pa-model"); err != nil || p.ID() != "codex" {
		t.Fatalf("pa-model phải về codex: %v %v", p, err)
	}
	if p, err := r.Route("pb-model"); err != nil || p.ID() != "claude" {
		t.Fatalf("pb-model phải về claude: %v %v", p, err)
	}
	_, err := r.Route("pc-model")
	if err == nil {
		t.Fatal("model lạ phải lỗi")
	}
	// Không bịa tiền tố từ tên provider ("codex-*" trong khi tiền tố thật là
	// "pa-"): chỉ nêu tên provider và chỉ chỗ xem model đúng.
	if msg := err.Error(); strings.Contains(msg, "-*") || !strings.Contains(msg, "GET /v1/models") || !strings.Contains(msg, "codex, claude") {
		t.Fatalf("thông báo 404 sai: %q", msg)
	}
}

func TestByProviderField(t *testing.T) {
	r := registry()
	// provider đích danh thắng tiền tố: model lạ nhưng provider="claude" thì
	// vẫn về claude.
	if p, err := r.ByID("claude"); err != nil || p.ID() != "claude" {
		t.Fatalf("ByID(claude) sai: %v %v", p, err)
	}
	if p, err := r.ByID("Codex"); err != nil || p.ID() != "codex" {
		t.Fatalf("ByID không phân biệt hoa thường phải vẫn khớp: %v %v", p, err)
	}
	if _, err := r.ByID("khong-co"); err == nil {
		t.Fatal("provider lạ phải lỗi")
	}
}
