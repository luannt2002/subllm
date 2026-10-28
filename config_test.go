package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateEnv — xoá mọi SUBLLM_* trong test, trả nguyên trạng khi xong (kể cả
// biến loadDotEnv đặt thêm).
func isolateEnv(t *testing.T) {
	t.Helper()
	saved := map[string]string{}
	unsetAll := func() {
		for _, kv := range os.Environ() {
			if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "SUBLLM_") {
				os.Unsetenv(k)
			}
		}
	}
	for _, kv := range os.Environ() {
		if k, v, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "SUBLLM_") {
			saved[k] = v
		}
	}
	unsetAll()
	t.Cleanup(func() {
		unsetAll()
		for k, v := range saved {
			os.Setenv(k, v)
		}
	})
}

// exampleKeys — các khoá trong .env.example theo thứ tự, kèm giá trị mẫu.
func exampleKeys(t *testing.T) ([]string, map[string]string) {
	t.Helper()
	f, err := os.Open(".env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var keys []string
	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		keys = append(keys, k)
		vals[k] = v
	}
	return keys, vals
}

// projectValue — DANH SÁCH TRẮNG khoá được có giá trị trong .env.example (do
// chính dự án quyết). Mọi khoá khác — bí mật, dữ liệu nhà cung cấp, kể cả
// khoá mới thêm sau này — phải để trống trong mẫu, giá trị chỉ ở .env.
func projectValue(key string) bool {
	switch key {
	case "SUBLLM_HOST", "SUBLLM_PORT", "SUBLLM_COOLDOWN", "SUBLLM_TIMEOUT",
		"SUBLLM_REFRESH_MARGIN", "SUBLLM_REFRESH_TIMEOUT", "SUBLLM_PROVIDERS":
		return true
	}
	for _, s := range []string{"_TYPE", "_ORIGINATOR", "_MAX_TOKENS"} {
		if strings.HasSuffix(key, s) {
			return true
		}
	}
	return false
}

// useExample — env = .env.example, khoá nhạy cảm (trống trong mẫu) điền giá
// trị giả hợp lệ; file credential mỗi provider một file riêng.
func useExample(t *testing.T) {
	t.Helper()
	isolateEnv(t)
	if err := loadDotEnv(".env.example", true); err != nil {
		t.Fatal(err)
	}
	keys, vals := exampleKeys(t)
	for _, k := range keys {
		switch {
		case k == "SUBLLM_API_KEY" || vals[k] != "":
		case strings.HasSuffix(k, "_URL"):
			os.Setenv(k, "https://example.invalid/x")
		case strings.HasSuffix(k, "_CRED_FILE"):
			os.Setenv(k, filepath.Join(t.TempDir(), k+".json"))
		default:
			os.Setenv(k, "gia-tri-gia")
		}
	}
}

func mustFail(t *testing.T, want string) {
	t.Helper()
	if _, err := configFromEnv(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("phải lỗi chứa %q, nhận: %v", want, err)
	}
}

// Cổng: mẫu chỉ chứa giá trị của dự án — ai điền bí mật hay dữ liệu nhà
// cung cấp vào .env.example (file được commit) thì đỏ.
func TestEnvExampleProjectValuesOnly(t *testing.T) {
	keys, vals := exampleKeys(t)
	blank := 0
	for _, k := range keys {
		if projectValue(k) {
			continue
		}
		blank++
		if vals[k] != "" {
			t.Errorf("%s có giá trị trong .env.example — chỉ khoá của dự án mới được, còn lại điền ở .env", k)
		}
	}
	if blank == 0 {
		t.Fatal("không thấy khoá 🔒 nào — mẫu sai định dạng?")
	}
}

// Cổng: .env.example đủ và không thừa — điền đủ thì dựng được; bỏ bất kỳ
// khoá nào (trừ API_KEY tuỳ chọn) thì lỗi; mọi adapter đều có provider mẫu.
func TestEnvExampleComplete(t *testing.T) {
	isolateEnv(t)
	mustFail(t, "SUBLLM_HOST, SUBLLM_PORT, SUBLLM_COOLDOWN, SUBLLM_PROVIDERS")

	useExample(t)
	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf(".env.example (điền giá trị giả) phải dựng được: %v", err)
	}
	if cfg.Host == "" || cfg.Port <= 0 || cfg.Cooldown <= 0 || len(cfg.Providers) == 0 {
		t.Fatalf("config dựng sai: %+v", cfg)
	}
	_, vals := exampleKeys(t)
	types := map[string]bool{}
	for _, name := range strings.Split(vals["SUBLLM_PROVIDERS"], ",") {
		types[vals["SUBLLM_PROVIDER_"+strings.ToUpper(strings.TrimSpace(name))+"_TYPE"]] = true
	}
	for typ := range adapters {
		if !types[typ] {
			t.Errorf("adapter %q không có provider nào trong .env.example — khối SUBLLM_TYPE_%s_* chưa được cổng này kiểm", typ, strings.ToUpper(typ))
		}
	}
	keys, _ := exampleKeys(t)
	for _, k := range keys {
		if k == "SUBLLM_API_KEY" {
			continue
		}
		v := os.Getenv(k)
		os.Unsetenv(k)
		if _, err := configFromEnv(); err == nil {
			t.Errorf("bỏ %s mà vẫn dựng được — khoá thừa trong .env.example?", k)
		}
		os.Setenv(k, v)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, tc := range []struct{ key, val, want string }{
		{"SUBLLM_HOST", "0.0.0.0", "SUBLLM_HOST (chỉ nhận loopback"},
		{"SUBLLM_HOST", "192.168.1.5", "SUBLLM_HOST (chỉ nhận loopback"},
		{"SUBLLM_PORT", "70000", "SUBLLM_PORT (phải ≤ 65535)"},
		{"SUBLLM_PORT", "0", "SUBLLM_PORT (phải là số nguyên dương)"},
		{"SUBLLM_COOLDOWN", "5", "SUBLLM_COOLDOWN (phải là thời lượng dương"},
		{"SUBLLM_TIMEOUT", "-1s", "SUBLLM_TIMEOUT (phải là thời lượng dương"},
		{"SUBLLM_TYPE_CLAUDE_BASE_URL", "api.example.com", "SUBLLM_TYPE_CLAUDE_BASE_URL | SUBLLM_BASE_URL (phải là URL http/https"},
		{"SUBLLM_TYPE_CODEX_TOKEN_URL", "ftp://x/y", "SUBLLM_TYPE_CODEX_TOKEN_URL | SUBLLM_TOKEN_URL (phải là URL http/https"},
		{"SUBLLM_TYPE_CODEX_BASE_URL", "https://x/y?a=1", "SUBLLM_TYPE_CODEX_BASE_URL | SUBLLM_BASE_URL (phải là URL http/https"},
		{"SUBLLM_TYPE_CODEX_BASE_URL", "https://x/y?", "SUBLLM_TYPE_CODEX_BASE_URL | SUBLLM_BASE_URL (phải là URL http/https"},
		{"SUBLLM_TYPE_CLAUDE_BASE_URL", "https://x/y#", "SUBLLM_TYPE_CLAUDE_BASE_URL | SUBLLM_BASE_URL (phải là URL http/https"},
		{"SUBLLM_TYPE_CLAUDE_MAX_TOKENS", "nhiều", "MAX_TOKENS (phải là số nguyên dương)"},
		{"SUBLLM_PROVIDER_CLAUDE_TYPE", "khong-co", `SUBLLM_PROVIDER_CLAUDE_TYPE="khong-co" (adapter có: claude, codex)`},
		{"SUBLLM_PROVIDERS", "codex,CODEX", `tên "CODEX" trùng`},
		{"SUBLLM_PROVIDERS", "co-dex", `tên "co-dex"`},
		{"SUBLLM_PROVIDERS", " , ", "SUBLLM_PROVIDERS (không có provider nào)"},
		{"SUBLLM_PROVIDERS", "acc3", "SUBLLM_PROVIDER_ACC3_TYPE"},
	} {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			useExample(t)
			os.Setenv(tc.key, tc.val)
			mustFail(t, tc.want)
		})
	}
	// loopback hợp lệ
	for _, host := range []string{"127.0.0.1", "::1", "localhost"} {
		useExample(t)
		os.Setenv("SUBLLM_HOST", host)
		if _, err := configFromEnv(); err != nil {
			t.Fatalf("host %s phải hợp lệ: %v", host, err)
		}
	}
	// URL có "/" cuối được bỏ để ghép path không ra "//"
	useExample(t)
	os.Setenv("SUBLLM_TYPE_CLAUDE_BASE_URL", "https://api.example.com/")
	if _, err := configFromEnv(); err != nil {
		t.Fatal(err)
	}
	if got := (env{prefixes: []string{"SUBLLM_TYPE_CLAUDE_"}, errs: new([]string)}).url("BASE_URL"); got != "https://api.example.com" {
		t.Fatalf("url phải bỏ / cuối, nhận %q", got)
	}
}

// Nhiều tài khoản chỉ bằng env: provider thứ hai khai TYPE + MODEL_PREFIX +
// CRED_FILE, còn lại lấy giá trị chung của kiểu; dùng chung file thì chặn.
func TestConfigMultiAccount(t *testing.T) {
	useExample(t)
	os.Setenv("SUBLLM_PROVIDERS", "codex, claude,claude2")
	os.Setenv("SUBLLM_PROVIDER_CLAUDE2_TYPE", "claude")
	os.Setenv("SUBLLM_PROVIDER_CLAUDE2_MODEL_PREFIX", "pb-")
	os.Setenv("SUBLLM_PROVIDER_CLAUDE2_CRED_FILE", filepath.Join(t.TempDir(), "acc2.json"))
	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf("claude2 phải dựng được chỉ với 3 biến riêng: %v", err)
	}
	if p, err := cfg.Providers.ByID("claude2"); err != nil || p.ID() != "claude2" {
		t.Fatalf("ByID(claude2) sai: %v %v", p, err)
	}
	if len(cfg.Warnings) != 3 || !strings.Contains(cfg.Warnings[2], `provider "claude2": file credential`) {
		t.Fatalf("file credential chưa có phải thành cảnh báo (không dừng): %q", cfg.Warnings)
	}
	os.Setenv("SUBLLM_PROVIDER_CLAUDE2_CRED_FILE", os.Getenv("SUBLLM_PROVIDER_CLAUDE_CRED_FILE"))
	mustFail(t, `SUBLLM_PROVIDER_CLAUDE2_CRED_FILE trùng file với provider "claude"`)
}

// Cùng một file viết theo nhiều cách vẫn phải bị coi là trùng: tương đối vs
// tuyệt đối, qua symlink tới file, qua thư mục cha là symlink khi file chưa có.
func TestConfigDuplicateCredFileSpellings(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "acc.json")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(dir, "hard.json")); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(t.TempDir(), "via")
	if err := os.Symlink(dir, linkDir); err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]string{
		"tương đối vs tuyệt đối":               {"acc.json", target},
		"tương đối vs tuyệt đối, file chưa có": {"chua-co.json", filepath.Join(dir, "chua-co.json")},
		"symlink tới file":                     {filepath.Join(dir, "link.json"), target},
		"hard link":                            {filepath.Join(dir, "hard.json"), target},
		"thư mục cha symlink, file mới":        {filepath.Join(linkDir, "moi.json"), filepath.Join(dir, "moi.json")},
	} {
		t.Run(name, func(t *testing.T) {
			useExample(t) // đọc .env.example ở thư mục dự án, trước khi đổi cwd
			wd, _ := os.Getwd()
			t.Cleanup(func() { os.Chdir(wd) })
			if err := os.Chdir(dir); err != nil { // để "acc.json" tương đối trỏ vào dir
				t.Fatal(err)
			}
			os.Setenv("SUBLLM_PROVIDERS", "claude,claude2")
			os.Setenv("SUBLLM_PROVIDER_CLAUDE2_TYPE", "claude")
			os.Setenv("SUBLLM_PROVIDER_CLAUDE2_MODEL_PREFIX", "pb-")
			os.Setenv("SUBLLM_PROVIDER_CLAUDE_CRED_FILE", pair[0])
			os.Setenv("SUBLLM_PROVIDER_CLAUDE2_CRED_FILE", pair[1])
			mustFail(t, `SUBLLM_PROVIDER_CLAUDE2_CRED_FILE trùng file với provider "claude"`)
		})
	}
}

func TestLoadDotEnv(t *testing.T) {
	isolateEnv(t)
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("# chú thích\n\nSUBLLM_T_A = một\nSUBLLM_T_B=x=y\nSUBLLM_T_C=từ-file\nSUBLLM_T_D=từ-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	os.Setenv("SUBLLM_T_C", "từ-env")
	os.Setenv("SUBLLM_T_D", "") // đặt mà rỗng = chưa đặt, file được điền
	if err := loadDotEnv(path, true); err != nil {
		t.Fatalf("loadDotEnv lỗi: %v", err)
	}
	for key, want := range map[string]string{"SUBLLM_T_A": "một", "SUBLLM_T_B": "x=y", "SUBLLM_T_C": "từ-env", "SUBLLM_T_D": "từ-file"} {
		if got := os.Getenv(key); got != want {
			t.Fatalf("%s = %q, muốn %q", key, got, want)
		}
	}
	missingFile := filepath.Join(t.TempDir(), "khong-co")
	if err := loadDotEnv(missingFile, false); err != nil {
		t.Fatalf("thiếu file mặc định phải bỏ qua, nhận lỗi: %v", err)
	}
	if err := loadDotEnv(missingFile, true); err == nil {
		t.Fatal("thiếu file chỉ đích danh (-env) phải lỗi")
	}
	for _, bad := range []string{"KHONG_CO_BANG\n", "=x\n", "SUBLLM_T_E=1\nSUBLLM_T_E=2\n", "export SUBLLM_T_F=1\n", "SUBLLM T=1\n"} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if err := loadDotEnv(path, true); err == nil {
			t.Fatalf("dòng %q phải lỗi", bad)
		}
	}
}
