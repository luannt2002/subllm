package main

// config.go — NƠI DUY NHẤT nạp env và đọc cấu hình. Mọi giá trị chạy (host,
// port, api key, cooldown, timeout, endpoint, client ID, phiên bản, tiền tố
// model, file credential…) đến từ env/.env; code không giữ giá trị mặc định
// nào. Thiếu hoặc sai (URL hỏng, port ngoài dải, thời lượng hỏng, host
// không phải loopback, file credential dùng chung…) → dừng lúc khởi động và
// liệt kê đủ một lượt.
//
// Định dạng (mẫu đủ ở .env.example). Provider tên <N> kiểu <T> đọc key <K>
// lần lượt ở:
//
//	SUBLLM_PROVIDER_<N>_<K>   riêng provider đó
//	SUBLLM_TYPE_<T>_<K>       chung cho mọi provider cùng kiểu
//	SUBLLM_<K>                chung cho mọi provider

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luannt2002/subllm/internal/provider"
	"github.com/luannt2002/subllm/internal/provider/claude"
	"github.com/luannt2002/subllm/internal/provider/codex"
)

// adapters — giao thức subllm biết nói, theo TYPE trong env. Thêm tài khoản
// / instance của giao thức đã có = chỉ sửa env; thêm giao thức mới = một
// package dưới internal/provider/ + một mục ở đây + khối SUBLLM_TYPE_<T>_*
// trong .env.example.
var adapters = map[string]func(name, credFile string, e env) provider.Provider{
	"codex": func(name, credFile string, e env) provider.Provider {
		return codex.New(codex.Config{
			Name:           name,
			CredFile:       credFile,
			ModelPrefix:    e.str("MODEL_PREFIX"),
			BaseURL:        e.url("BASE_URL"),
			TokenURL:       e.url("TOKEN_URL"),
			ClientID:       e.str("CLIENT_ID"),
			ClientVersion:  e.str("CLIENT_VERSION"),
			Originator:     e.str("ORIGINATOR"),
			Beta:           e.str("BETA"),
			UserAgent:      e.str("USER_AGENT"),
			Timeout:        e.duration("TIMEOUT"),
			RefreshMargin:  e.duration("REFRESH_MARGIN"),
			RefreshTimeout: e.duration("REFRESH_TIMEOUT"),
		})
	},
	"claude": func(name, credFile string, e env) provider.Provider {
		return claude.New(claude.Config{
			Name:           name,
			CredFile:       credFile,
			ModelPrefix:    e.str("MODEL_PREFIX"),
			BaseURL:        e.url("BASE_URL"),
			TokenURL:       e.url("TOKEN_URL"),
			ClientID:       e.str("CLIENT_ID"),
			APIVersion:     e.str("API_VERSION"),
			Beta:           e.str("BETA"),
			UserAgent:      e.str("USER_AGENT"),
			MaxTokens:      e.positiveInt("MAX_TOKENS"),
			Timeout:        e.duration("TIMEOUT"),
			RefreshMargin:  e.duration("REFRESH_MARGIN"),
			RefreshTimeout: e.duration("REFRESH_TIMEOUT"),
		})
	},
}

// config — mọi thứ subllm đọc từ env.
type config struct {
	Host      string
	Port      int
	APIKey    string // rỗng = không kiểm key
	Cooldown  time.Duration
	Providers provider.Registry
	// Warnings — cấu hình hợp lệ nhưng chưa dùng được ngay (file credential
	// chưa có): không dừng khởi động vì CLI login / công cụ đổi tài khoản có thể tạo hoặc
	// dời file sau; request tới tài khoản đó lỗi rõ ràng + failover.
	Warnings []string
}

// validName — tên provider nằm trong tên biến env.
var validName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// envKey — tên biến hợp lệ trong .env.
var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// configFromEnv — dựng config từ env; thiếu/sai biến nào thì lỗi liệt kê đủ
// một lượt.
func configFromEnv() (config, error) {
	var errs []string
	top := env{prefixes: []string{"SUBLLM_"}, errs: &errs}
	cfg := config{
		Host:     top.loopbackHost("HOST"),
		Port:     top.port("PORT"),
		APIKey:   top.optional("API_KEY"),
		Cooldown: top.duration("COOLDOWN"),
	}
	seenName := map[string]bool{}
	credOwner := map[string]string{} // file credential → provider giữ nó
	type credFile struct {
		name string
		info os.FileInfo
	}
	var credFiles []credFile // để bắt hard link: khác đường dẫn, cùng một file
	for _, name := range strings.Split(top.str("PROVIDERS"), ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		upper := strings.ToUpper(name)
		if !validName.MatchString(name) || seenName[upper] {
			errs = append(errs, fmt.Sprintf("SUBLLM_PROVIDERS (tên %q trùng hoặc có ký tự ngoài A-Z 0-9 _)", name))
			continue
		}
		seenName[upper] = true
		inst := "SUBLLM_PROVIDER_" + upper + "_"
		typ := strings.ToLower(top.under(inst).str("TYPE"))
		build, ok := adapters[typ]
		if !ok {
			if typ != "" {
				errs = append(errs, fmt.Sprintf("%sTYPE=%q (adapter có: %s)", inst, typ, adapterNames()))
			}
			continue
		}
		e := top.under(inst, "SUBLLM_TYPE_"+strings.ToUpper(typ)+"_", "SUBLLM_")
		cred := e.path("CRED_FILE")
		if cred != "" {
			// Hai provider cùng một file = hai khoá refresh riêng trên cùng
			// refresh token xoay vòng → giẫm nhau, văng đăng nhập.
			key := canonicalPath(cred)
			other, dup := credOwner[key]
			info, statErr := os.Stat(cred)
			for _, f := range credFiles {
				if !dup && statErr == nil && os.SameFile(f.info, info) {
					other, dup = f.name, true
				}
			}
			if dup {
				errs = append(errs, fmt.Sprintf("%sCRED_FILE trùng file với provider %q — mỗi file credential chỉ một provider giữ", inst, other))
			}
			credOwner[key] = name
			if statErr != nil {
				cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("provider %q: file credential %s chưa dùng được (%v)", name, cred, statErr))
			} else {
				credFiles = append(credFiles, credFile{name, info})
			}
		}
		cfg.Providers = append(cfg.Providers, build(name, cred, e))
	}
	// PROVIDERS có đặt mà toàn tên rỗng ("," …) cũng là thiếu.
	if len(cfg.Providers) == 0 && len(errs) == 0 {
		errs = append(errs, "SUBLLM_PROVIDERS (không có provider nào)")
	}
	if len(errs) > 0 {
		return cfg, fmt.Errorf("thiếu/sai biến môi trường: %s — xem .env.example", strings.Join(errs, ", "))
	}
	return cfg, nil
}

func adapterNames() string {
	names := make([]string, 0, len(adapters))
	for n := range adapters {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// canonicalPath — đường dẫn so trùng được: tuyệt đối, theo symlink (của
// file nếu đã có, không thì của thư mục cha).
func canonicalPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// env — bộ đọc env: key K thử lần lượt ở prefixes[i]+K (giá trị rỗng =
// chưa đặt); lỗi dồn vào errs chung để báo một lượt.
type env struct {
	prefixes []string
	errs     *[]string
}

// under — bộ đọc cùng errs với danh sách tiền tố khác.
func (e env) under(prefixes ...string) env { return env{prefixes: prefixes, errs: e.errs} }

func (e env) fail(key, why string) {
	names := make([]string, len(e.prefixes))
	for i, p := range e.prefixes {
		names[i] = p + key
	}
	*e.errs = append(*e.errs, strings.Join(names, " | ")+why)
}

func (e env) optional(key string) string {
	for _, p := range e.prefixes {
		if v := os.Getenv(p + key); v != "" {
			return v
		}
	}
	return ""
}

func (e env) str(key string) string {
	v := e.optional(key)
	if v == "" {
		e.fail(key, "")
	}
	return v
}

func (e env) positiveInt(key string) int {
	v := e.str(key)
	n, err := strconv.Atoi(v)
	if v != "" && (err != nil || n <= 0) {
		e.fail(key, " (phải là số nguyên dương)")
	}
	return n
}

func (e env) port(key string) int {
	n := e.positiveInt(key)
	if n > 65535 {
		e.fail(key, " (phải ≤ 65535)")
	}
	return n
}

func (e env) duration(key string) time.Duration {
	v := e.str(key)
	d, err := time.ParseDuration(v)
	if v != "" && (err != nil || d <= 0) {
		e.fail(key, " (phải là thời lượng dương, vd 30s, 5m)")
	}
	return d
}

// url — URL http/https đầy đủ, bỏ "/" cuối để ghép path không ra "//".
func (e env) url(key string) string {
	v := e.str(key)
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(v, "?#") {
		e.fail(key, " (phải là URL http/https đầy đủ, không query/fragment — path được ghép thêm)")
		return ""
	}
	return strings.TrimRight(v, "/")
}

// path — đường dẫn file, ~ đầu chuỗi đổi thành thư mục nhà.
func (e env) path(key string) string {
	p := e.str(key)
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// loopbackHost — chỉ nhận loopback: token là chìa khoá tài khoản cá nhân,
// hở ra mạng là ai trong LAN cũng tiêu được gói của bạn.
func (e env) loopbackHost(key string) string {
	v := e.str(key)
	if v == "" || v == "localhost" {
		return v
	}
	if ip := net.ParseIP(v); ip == nil || !ip.IsLoopback() {
		e.fail(key, " (chỉ nhận loopback: 127.0.0.1, ::1, localhost)")
	}
	return v
}

// loadDotEnv — nạp từng dòng KEY=VALUE của path vào env; biến env đã đặt
// (khác rỗng) thì giữ nguyên — env thật thắng file. Dòng trống / bắt đầu
// bằng # bị bỏ qua; không bóc dấu nháy, không nhận chú thích cuối dòng;
// khoá lặp trong file là lỗi (không đoán dòng nào thắng). Không có file: bỏ
// qua, trừ khi required (người chạy chỉ đích danh file).
func loadDotEnv(path string, required bool) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return err
	}
	seen := map[string]int{} // khoá → dòng đầu tiên
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKey.MatchString(key) {
			return fmt.Errorf("%s dòng %d: phải là KEY=VALUE (KEY gồm A-Z a-z 0-9 _, không \"export\")", path, i+1)
		}
		if first, dup := seen[key]; dup {
			return fmt.Errorf("%s: khoá %s lặp ở dòng %d và %d", path, key, first, i+1)
		}
		seen[key] = i + 1
		if os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, strings.TrimSpace(val)); err != nil {
			return fmt.Errorf("%s dòng %d: %w", path, i+1, err)
		}
	}
	return nil
}
