package tokenstore

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// makeJWT — dựng JWT giả: header.payload.signature, payload chứa exp.
func makeJWT(t *testing.T, exp int64) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"exp": exp})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return "aaa." + body + ".bbb"
}

func TestJWTExp(t *testing.T) {
	want := time.Unix(1792747926, 0)
	got, err := jwtExp(makeJWT(t, 1792747926))
	if err != nil {
		t.Fatalf("jwtExp lỗi: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("jwtExp = %v, muốn %v", got, want)
	}
	if _, err := jwtExp("khong-phai-jwt"); err == nil {
		t.Fatal("jwtExp phải lỗi với chuỗi lạ")
	}
}
