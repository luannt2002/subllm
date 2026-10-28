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

// jwtAt — access token dạng JWT hết hạn lúc exp (chỉ claim exp, không ký).
func jwtAt(exp time.Time) string {
	p, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
	return "h." + base64URL(p) + ".s"
}

// codexStore — cấu hình như provider kiểu codex (JWT + account_id + last_refresh).
func codexStore(path, refreshURL string) *Store {
	s := New(path, "tokens", 5*time.Minute, 5*time.Second)
	s.AccessKey, s.RefreshKey, s.AccountKey, s.LastRefreshKey = "access_token", "refresh_token", "account_id", "last_refresh"
	s.RefreshURL, s.ClientID = refreshURL, "cid"
	return s
}

// readDoc — đọc lại file credential sau khi Store ghi.
func readDoc(t *testing.T, path string) (doc, tok map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("file không phải JSON: %v", err)
	}
	return doc, doc["tokens"].(map[string]any)
}

// Client huỷ request lúc đang refresh: server OAuth đã nhận refresh token cũ
// (đã xoay) nên subllm vẫn phải nhận phản hồi và ghi token mới, không thì
// file giữ token đã chết → mất đăng nhập.
func TestRefreshSurvivesClientCancel(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{"access_token": jwtAt(time.Now().Add(time.Hour)), "refresh_token": "rt1"})
	}))
	defer srv.Close()
	path := writeFile(t, `{"tokens":{"access_token":"`+jwtAt(time.Now().Add(-time.Hour))+`","refresh_token":"rt0"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	codexStore(path, srv.URL).Get(ctx)
	if _, tok := readDoc(t, path); n.Load() != 1 || tok["refresh_token"] != "rt1" {
		t.Fatalf("server nhận %d refresh, file giữ refresh_token=%v (phải rt1)", n.Load(), tok["refresh_token"])
	}
}

// File credential là symlink (công cụ đổi tài khoản trỏ auth.json sang file
// của từng tài khoản): refresh phải ghi vào file thật và giữ nguyên symlink.
func TestRefreshWritesThroughSymlink(t *testing.T) {
	srv, _, _ := refreshServer(t, map[string]any{"access_token": jwtAt(time.Now().Add(time.Hour)), "refresh_token": "rt1"})
	dir := t.TempDir()
	real := filepath.Join(dir, "work.json")
	if err := os.WriteFile(real, []byte(`{"tokens":{"access_token":"`+jwtAt(time.Now().Add(-time.Hour))+`","refresh_token":"rt0"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "auth.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := codexStore(link, srv.URL).Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink phải còn nguyên: %v %v", fi.Mode(), err)
	}
	if _, tok := readDoc(t, real); tok["refresh_token"] != "rt1" {
		t.Fatalf("file thật phải có token mới, nhận refresh_token=%v", tok["refresh_token"])
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*subllm*")); len(left) != 0 {
		t.Fatalf("không được để sót tệp tạm: %v", left)
	}
}

// Codex CLI để last_refresh ở GỐC file (đã đối chiếu file auth thật): ghi vào
// trong tokens thì CLI vẫn thấy mốc cũ và tự refresh thêm, xoay token lần nữa.
func TestLastRefreshAtRoot(t *testing.T) {
	srv, _, _ := refreshServer(t, map[string]any{"access_token": jwtAt(time.Now().Add(time.Hour)), "refresh_token": "rt1"})
	path := writeFile(t, `{"auth_mode":"chatgpt","last_refresh":"2020-01-01T00:00:00Z","tokens":{"access_token":"`+jwtAt(time.Now().Add(-time.Hour))+`","refresh_token":"rt0"}}`)
	if _, err := codexStore(path, srv.URL).Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc, tok := readDoc(t, path)
	at, err := time.Parse(time.RFC3339Nano, fmt.Sprint(doc["last_refresh"]))
	if err != nil || time.Since(at) > time.Minute || tok["last_refresh"] != nil || doc["auth_mode"] != "chatgpt" {
		t.Fatalf("last_refresh gốc phải là giờ refresh vừa rồi, không nằm trong tokens: gốc=%v tokens=%v", doc["last_refresh"], tok["last_refresh"])
	}
}
