package tokenstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// refreshServer — server refresh giả: đếm số lần gọi, ghi lại body, trả resp.
func refreshServer(t *testing.T, resp map[string]any) (*httptest.Server, *atomic.Int32, *[]string) {
	t.Helper()
	var n atomic.Int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		raw := make([]byte, r.ContentLength)
		r.Body.Read(raw)
		bodies = append(bodies, r.Header.Get("Content-Type")+" "+string(raw))
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &n, &bodies
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cred.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// claudeStore — cấu hình như provider kiểu claude (token opaque + expiresAt ms).
func claudeStore(path, refreshURL string) *Store {
	s := New(path, "claudeAiOauth", 5*time.Minute, 5*time.Second)
	s.AccessKey, s.RefreshKey, s.ExpiresAtMsKey = "accessToken", "refreshToken", "expiresAt"
	s.RefreshURL, s.ClientID, s.JSONBody = refreshURL, "cid", true
	return s
}

// P1: token opaque (không phải JWT) → hạn mới phải lấy từ expires_in của
// phản hồi refresh; nếu không, mọi lần Get sau lại refresh (xoay refresh
// token liên tục, giẫm lên các phiên CLI dùng chung file).
func TestRefreshOpaqueTokenOnlyOnce(t *testing.T) {
	past := time.Now().Add(-time.Hour).UnixMilli()
	path := writeFile(t, fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"opaque-old","refreshToken":"rt0","expiresAt":%d,"scopes":["a"]},"other":1}`, past))
	srv, n, bodies := refreshServer(t, map[string]any{"access_token": "opaque-new", "refresh_token": "rt1", "expires_in": 28800})
	s := claudeStore(path, srv.URL)
	for i := 0; i < 3; i++ {
		c, err := s.Get(context.Background())
		if err != nil || c.AccessToken != "opaque-new" {
			t.Fatalf("Get #%d = %+v, %v", i, c, err)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("3 lần Get phải refresh đúng 1 lần, nhận %d", n.Load())
	}
	var doc map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("file sau refresh không phải JSON: %v", err)
	}
	tok := doc["claudeAiOauth"].(map[string]any)
	exp := time.UnixMilli(int64(tok["expiresAt"].(float64)))
	if d := time.Until(exp); d < 7*time.Hour || d > 9*time.Hour {
		t.Fatalf("expiresAt phải ≈ now+8h (expires_in), nhận %v", d)
	}
	if tok["refreshToken"] != "rt1" || tok["scopes"] == nil || doc["other"] == nil {
		t.Fatalf("ghi ngược phải giữ field khác + thay refresh token: %s", raw)
	}
	var body map[string]string
	json.Unmarshal([]byte((*bodies)[0][len("application/json "):]), &body)
	if body["grant_type"] != "refresh_token" || body["refresh_token"] != "rt0" || body["client_id"] != "cid" {
		t.Fatalf("body refresh JSON sai: %s", (*bodies)[0])
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
		t.Fatalf("file credential phải giữ 0600, nhận %v", info.Mode().Perm())
	}
}

// P2: phản hồi refresh không có account_id → request kích refresh vẫn phải
// mang account_id đang có trong file.
func TestRefreshKeepsAccountID(t *testing.T) {
	jwt := func(exp time.Time) string {
		p, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
		return "h." + base64URL(p) + ".s"
	}
	path := writeFile(t, `{"tokens":{"access_token":"`+jwt(time.Now().Add(-time.Hour))+`","refresh_token":"rt0","account_id":"acc-1"}}`)
	srv, _, bodies := refreshServer(t, map[string]any{"access_token": jwt(time.Now().Add(time.Hour)), "refresh_token": "rt1"})
	s := New(path, "tokens", 5*time.Minute, 5*time.Second)
	s.AccessKey, s.RefreshKey, s.AccountKey, s.LastRefreshKey = "access_token", "refresh_token", "account_id", "last_refresh"
	s.RefreshURL, s.ClientID = srv.URL, "cid"
	c, err := s.Get(context.Background())
	if err != nil || c.AccountID != "acc-1" {
		t.Fatalf("request kích refresh phải giữ AccountID=acc-1, nhận %+v %v", c, err)
	}
	form, _ := url.ParseQuery((*bodies)[0][len("application/x-www-form-urlencoded "):])
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "rt0" || form.Get("client_id") != "cid" {
		t.Fatalf("body refresh form sai: %s", (*bodies)[0])
	}
}

func TestRefreshRejected(t *testing.T) {
	path := writeFile(t, fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"x","refreshToken":"rt0","expiresAt":%d}}`, time.Now().Add(-time.Hour).UnixMilli()))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer srv.Close()
	if _, err := claudeStore(path, srv.URL).Get(context.Background()); err == nil {
		t.Fatal("refresh bị từ chối phải lỗi")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) == "" || !json.Valid(raw) {
		t.Fatalf("refresh lỗi không được làm hỏng file: %q", raw)
	}
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
