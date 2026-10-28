// Package tokenstore đọc/ghi file credential của CLI chính thức và refresh
// token, dùng chung cho mọi provider. Các file credential chỉ khác tên field
// và endpoint refresh, nên khai bằng cấu hình Store thay vì viết nhiều bộ
// copy-paste.
//
// Quy ước an toàn: ghi qua tệp tạm + rename (atomic), giữ permission 0600,
// không bao giờ log token.
package tokenstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store — một file credential: token nằm ở object nào, field tên gì,
// refresh ở đâu.
type Store struct {
	// Path — đường dẫn file credential của CLI chính thức.
	Path string
	// TokenAt — tên field của object chứa token, vd "tokens" hoặc
	// "claudeAiOauth".
	TokenAt string
	// AccessKey / RefreshKey / AccountKey — tên field trong object token.
	AccessKey  string
	RefreshKey string
	AccountKey string // rỗng nếu backend không cần
	// ExpiresAtMsKey — field hạn tính bằng mili-giây (claude); rỗng nếu
	// không dùng.
	ExpiresAtMsKey string
	// LastRefreshKey — field mốc thời gian refresh ở GỐC file (codex); rỗng
	// nếu không dùng.
	LastRefreshKey string
	// RefreshURL — endpoint đổi refresh_token lấy token mới.
	RefreshURL string
	// ClientID — client ID công khai của CLI chính thức (Codex/Claude Code).
	ClientID string
	// JSONBody — refresh gửi JSON thay vì form-urlencoded.
	JSONBody bool
	// Margin — token còn hạn dưới mức này thì refresh trước.
	Margin time.Duration

	mu       sync.Mutex // một file credential, một khóa
	client   *http.Client
	rejected string // refresh token server đã từ chối hẳn: không gửi lại
}

// rejectedError — server OAuth từ chối hẳn refresh token (400/401/403, vd bị
// thu hồi): thử lại vô ích, phải login lại. 429/5xx/mạng là lỗi tạm.
type rejectedError struct{ status int }

func (e *rejectedError) Error() string {
	return fmt.Sprintf("refresh bị từ chối (HTTP %d) — login lại bằng CLI", e.status)
}

// New — Store cho file path, token nằm ở object tokenAt; refresh trước khi
// hết hạn margin, mỗi lượt refresh tối đa timeout. Các field còn lại do
// provider đặt.
func New(path, tokenAt string, margin, timeout time.Duration) *Store {
	return &Store{
		Path: path, TokenAt: tokenAt, Margin: margin,
		client: &http.Client{Timeout: timeout},
	}
}

// Credentials — thứ provider cần để gọi backend.
type Credentials struct {
	AccessToken string
	AccountID   string // codex: account_id; claude: rỗng
}

// Get — trả token còn hạn; sắp hết hạn (< Margin) thì refresh rồi ghi
// ngược file. Khóa theo file để nhiều request không refresh đè nhau.
func (s *Store) Get(ctx context.Context) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tok, doc, err := s.load()
	if err != nil {
		return Credentials{}, err
	}
	access := field(tok, s.AccessKey)
	if access == "" {
		return Credentials{}, fmt.Errorf("%s không có %s — chạy CLI login lại", s.Path, s.AccessKey)
	}

	expiry, expErr := tokenExpiry(access, tok, s.ExpiresAtMsKey)
	if expErr == nil && time.Until(expiry) >= s.Margin {
		return Credentials{AccessToken: access, AccountID: field(tok, s.AccountKey)}, nil
	}

	refreshTok := field(tok, s.RefreshKey)
	if refreshTok == "" {
		return Credentials{}, fmt.Errorf("token sắp hết hạn mà không có %s — login lại bằng CLI", s.RefreshKey)
	}
	var fresh refreshedToken
	if refreshTok == s.rejected {
		// Server đã từ chối đúng token này: gửi lại chỉ tốn một vòng mạng trong
		// lúc giữ khoá. Login lại thì file có token khác → refresh bình thường.
		err = fmt.Errorf("refresh token trong %s đã bị từ chối — login lại bằng CLI", s.Path)
	} else {
		// Không huỷ theo client: server OAuth có thể đã xoay token, bỏ phản hồi
		// là file giữ refresh token đã chết. Vẫn bị chặn bởi timeout refresh.
		fresh, err = s.refresh(context.WithoutCancel(ctx), refreshTok)
		var rej *rejectedError
		if errors.As(err, &rej) {
			s.rejected = refreshTok
		}
	}
	if err != nil {
		if expErr == nil && time.Until(expiry) > 0 {
			// Token hiện tại còn hạn: dùng tạm, request sau thử refresh lại.
			log.Printf("refresh %s lỗi, dùng tạm token còn hạn %s: %v", s.Path, time.Until(expiry).Round(time.Second), err)
			return Credentials{AccessToken: access, AccountID: field(tok, s.AccountKey)}, nil
		}
		return Credentials{}, err
	}
	// Ghi ngược đúng các field của file đó: CLI chính thức (codex/claude)
	// đọc cùng file nên hưởng token tươi theo.
	tok[s.AccessKey] = fresh.AccessToken
	tok[s.RefreshKey] = fresh.RefreshToken
	if s.ExpiresAtMsKey != "" {
		// Token opaque (không phải JWT) chỉ biết hạn qua expires_in; thiếu
		// bước này thì mọi lần Get sau lại refresh, xoay refresh token liên tục.
		if fresh.ExpiresIn > 0 {
			tok[s.ExpiresAtMsKey] = time.Now().Add(time.Duration(fresh.ExpiresIn) * time.Second).UnixMilli()
		} else if t, err := jwtExp(fresh.AccessToken); err == nil {
			tok[s.ExpiresAtMsKey] = t.UnixMilli()
		} else {
			log.Printf("cảnh báo: refresh %s không cho biết hạn token mới (không expires_in, không phải JWT) — lần gọi sau sẽ refresh lại", s.Path)
		}
	}
	if s.LastRefreshKey != "" {
		doc[s.LastRefreshKey] = time.Now().UTC().Format(time.RFC3339Nano) // gốc file, như Codex CLI ghi
	}
	if err := s.save(doc, tok); err != nil {
		return Credentials{}, fmt.Errorf("ghi ngược token vào %s: %w", s.Path, err)
	}
	// Phản hồi refresh thường không mang account_id: giữ cái đang có trong file.
	accountID := fresh.AccountID
	if accountID == "" {
		accountID = field(tok, s.AccountKey)
	}
	return Credentials{AccessToken: fresh.AccessToken, AccountID: accountID}, nil
}

// field — rút một field chuỗi từ object token; key rỗng → "".
func field(tok map[string]any, key string) string {
	if key == "" {
		return ""
	}
	v, _ := tok[key].(string)
	return v
}

// load — đọc file, trả về (object token, toàn bộ document gốc).
func (s *Store) load() (map[string]any, map[string]any, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("đọc %s: %w", s.Path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse JSON %s: %w", s.Path, err)
	}
	tok, ok := doc[s.TokenAt].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("%s thiếu object %q — chưa login?", s.Path, s.TokenAt)
	}
	return tok, doc, nil
}

// save — ghi nguyên document gốc với object token đã cập nhật, atomic.
func (s *Store) save(doc map[string]any, tok map[string]any) error {
	doc[s.TokenAt] = tok
	clean, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	// Path là symlink (công cụ đổi tài khoản) thì ghi vào file thật: rename
	// lên chính symlink sẽ thay nó bằng file thường, file thật giữ token chết.
	path := s.Path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	// Tệp tạm tên riêng mỗi lần (0600), cùng thư mục để rename là atomic.
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".subllm-*")
	if err != nil {
		return fmt.Errorf("tạo tệp tạm cạnh %s: %w", path, err)
	}
	tmp := f.Name()
	_, err = f.Write(append(clean, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("ghi tệp tạm %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("thay %s: %w", path, err)
	}
	return nil
}

// tokenExpiry — hạn của token: ưu tiên claim "exp" trong JWT; thiếu thì
// field mili-giây (kiểu claude).
func tokenExpiry(access string, tok map[string]any, msKey string) (time.Time, error) {
	if t, err := jwtExp(access); err == nil {
		return t, nil
	}
	if msKey != "" {
		if ms, ok := tok[msKey].(float64); ok && ms > 0 {
			return time.UnixMilli(int64(ms)), nil
		}
	}
	return time.Time{}, fmt.Errorf("không xác định được thời hạn token")
}

// refreshedToken — kết quả một lượt refresh.
type refreshedToken struct {
	AccessToken  string
	RefreshToken string
	AccountID    string
	ExpiresIn    int64 // giây, 0 nếu server không trả
}

// refresh — đổi refresh_token lấy token mới. Server đôi khi không trả
// refresh_token mới: giữ cái cũ.
func (s *Store) refresh(ctx context.Context, refreshToken string) (refreshedToken, error) {
	params := map[string]string{"grant_type": "refresh_token", "refresh_token": refreshToken, "client_id": s.ClientID}
	var body io.Reader
	contentType := "application/x-www-form-urlencoded"
	if s.JSONBody {
		raw, _ := json.Marshal(params)
		body, contentType = bytes.NewReader(raw), "application/json"
	} else {
		form := url.Values{}
		for k, v := range params {
			form.Set(k, v)
		}
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.RefreshURL, body)
	if err != nil {
		return refreshedToken{}, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := s.client.Do(req)
	if err != nil {
		return refreshedToken{}, fmt.Errorf("gọi %s: %w", s.RefreshURL, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return refreshedToken{}, &rejectedError{status: resp.StatusCode}
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return refreshedToken{}, fmt.Errorf("refresh %s trả JSON lạ (HTTP %d): %w", s.RefreshURL, resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		return refreshedToken{}, fmt.Errorf("refresh bị từ chối (HTTP %d) — login lại bằng CLI", resp.StatusCode)
	}
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	return refreshedToken{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, AccountID: out.AccountID, ExpiresIn: out.ExpiresIn}, nil
}
