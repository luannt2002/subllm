package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fake — provider giả: trả lần lượt các lỗi trong errs (nil = thành công,
// phát một mẩu chữ); midStream = phát chữ rồi mới lỗi.
type fake struct {
	id, prefix string
	errs       []error
	midStream  bool
	calls      int
}

func (f *fake) ID() string                                   { return f.id }
func (f *fake) Match(m string) bool                          { return strings.HasPrefix(m, f.prefix) }
func (f *fake) ListModels(context.Context) ([]string, error) { return nil, nil }
func (f *fake) StreamChat(_ context.Context, _ ChatRequest, emit func(StreamEvent) error) error {
	err := f.errs[f.calls%len(f.errs)]
	f.calls++
	if err == nil || f.midStream {
		emit(StreamEvent{Text: f.id})
	}
	return err
}

func run(t *testing.T, r *Router, req ChatRequest) (string, error) {
	t.Helper()
	plan, err := r.Plan(req)
	if err != nil {
		return "", err
	}
	var got strings.Builder
	err = r.Stream(context.Background(), plan, req, func(ev StreamEvent) error {
		got.WriteString(ev.Text)
		return nil
	})
	return got.String(), err
}

func TestRouterFailover(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	limited := &UpstreamError{Status: http.StatusTooManyRequests, RetryAfter: time.Minute, Msg: "429"}
	acc1 := &fake{id: "acc1", prefix: "m-", errs: []error{limited}}
	acc2 := &fake{id: "acc2", prefix: "m-", errs: []error{nil}}
	r := NewRouter(Registry{acc1, acc2}, 5*time.Minute)
	r.now = func() time.Time { return now }
	req := ChatRequest{Model: "m-x"}

	// acc1 dính 429 → acc2 trả lời; acc1 nghỉ đúng Retry-After.
	if got, err := run(t, r, req); err != nil || got != "acc2" {
		t.Fatalf("phải failover sang acc2: %q %v", got, err)
	}
	st := r.Status()
	if st[0].CoolingUntil == nil || !st[0].CoolingUntil.Equal(now.Add(time.Minute)) || st[0].Failovers != 1 || st[1].OK != 1 || st[1].CoolingUntil != nil {
		t.Fatalf("trạng thái sai: %+v", st)
	}
	// Trong lúc acc1 nghỉ: không gọi acc1 nữa.
	run(t, r, req)
	if acc1.calls != 1 || acc2.calls != 2 {
		t.Fatalf("acc1 đang nghỉ không được gọi: acc1=%d acc2=%d", acc1.calls, acc2.calls)
	}
	// Hết nghỉ → acc1 được thử lại trước (thứ tự ưu tiên).
	now = now.Add(2 * time.Minute)
	acc1.errs = []error{nil}
	if got, _ := run(t, r, req); got != "acc1" {
		t.Fatalf("hết nghỉ phải quay lại acc1, nhận %q", got)
	}
}

func TestRouterAllCoolingReturns429(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	limited := &UpstreamError{Status: http.StatusTooManyRequests, Msg: "429"}
	acc1 := &fake{id: "acc1", prefix: "m-", errs: []error{limited}}
	acc2 := &fake{id: "acc2", prefix: "m-", errs: []error{limited}}
	r := NewRouter(Registry{acc1, acc2}, 5*time.Minute)
	r.now = func() time.Time { return now }
	_, err := run(t, r, ChatRequest{Model: "m-x"})
	if err == nil || !strings.HasPrefix(err.Error(), "acc2: ") {
		t.Fatalf("hết tài khoản phải trả lỗi của acc2 (mang tên): %v", err)
	}
	// Cả hai đang nghỉ (cooldown mặc định vì không có Retry-After) → 429 ngay.
	_, err = run(t, r, ChatRequest{Model: "m-x"})
	var up *UpstreamError
	if !errors.As(err, &up) || up.Status != 429 || up.RetryAfter != 5*time.Minute || acc1.calls != 1 {
		t.Fatalf("phải trả 429 không gọi upstream, RetryAfter=5m: %v calls=%d", err, acc1.calls)
	}
	// Chọn đích danh thì vẫn gọi dù đang nghỉ.
	run(t, r, ChatRequest{Model: "m-x", Provider: "ACC1"})
	if acc1.calls != 2 {
		t.Fatalf("đích danh phải bỏ qua thời gian nghỉ: calls=%d", acc1.calls)
	}
}

func TestRouterNoFailover(t *testing.T) {
	for name, tc := range map[string]struct {
		err       error
		midStream bool
		pinned    bool
	}{
		"lỗi 400 (request sai, acc nào cũng sai)": {err: &UpstreamError{Status: 400, Msg: "400"}},
		"lỗi 5xx (backend chung sập)":             {err: &UpstreamError{Status: 500, Msg: "500"}},
		"đã stream dở rồi mới lỗi":                {err: &AccountError{Err: errors.New("x")}, midStream: true},
		"chọn đích danh":                          {err: &AccountError{Err: errors.New("x")}, pinned: true},
	} {
		acc1 := &fake{id: "acc1", prefix: "m-", errs: []error{tc.err}, midStream: tc.midStream}
		acc2 := &fake{id: "acc2", prefix: "m-", errs: []error{nil}}
		r := NewRouter(Registry{acc1, acc2}, time.Minute)
		req := ChatRequest{Model: "m-x"}
		if tc.pinned {
			req.Provider = "acc1"
		}
		if _, err := run(t, r, req); err == nil || acc2.calls != 0 {
			t.Fatalf("%s: không được failover (err=%v acc2=%d)", name, err, acc2.calls)
		}
	}
	// Lỗi tài khoản (credential, 401, 403) → failover, nhưng không bắt nghỉ.
	for _, err := range []error{&AccountError{Err: errors.New("thiếu file")}, &UpstreamError{Status: 401}, &UpstreamError{Status: 403}} {
		acc1 := &fake{id: "acc1", prefix: "m-", errs: []error{err}}
		acc2 := &fake{id: "acc2", prefix: "m-", errs: []error{nil}}
		r := NewRouter(Registry{acc1, acc2}, time.Minute)
		if got, e := run(t, r, ChatRequest{Model: "m-x"}); e != nil || got != "acc2" {
			t.Fatalf("%v phải failover: %q %v", err, got, e)
		}
		if r.Status()[0].CoolingUntil != nil {
			t.Fatalf("%v không được bắt nghỉ", err)
		}
	}
}

// Lỗi do client (bỏ đi giữa chừng, request không dịch được) không phải lỗi
// của tài khoản: không failover, không đếm vào /status.
func TestRouterClientErrorsNotCounted(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		cancel bool
	}{
		"client huỷ (ctx canceled)":  {err: &AccountError{Err: errors.New("context canceled")}, cancel: true},
		"request không dịch được":    {err: &RequestError{Err: errors.New("role lạ")}},
		"upstream 400 (request sai)": {err: &UpstreamError{Status: 400, Msg: "HTTP 400"}},
	} {
		acc1 := &fake{id: "acc1", prefix: "m-", errs: []error{tc.err}}
		acc2 := &fake{id: "acc2", prefix: "m-", errs: []error{nil}}
		r := NewRouter(Registry{acc1, acc2}, time.Minute)
		ctx, cancel := context.WithCancel(context.Background())
		if tc.cancel {
			cancel()
		}
		req := ChatRequest{Model: "m-x"}
		plan, _ := r.Plan(req)
		err := r.Stream(ctx, plan, req, func(StreamEvent) error { return nil })
		cancel()
		if err == nil || acc2.calls != 0 || r.Status()[0].Failed != 0 || r.Status()[0].LastError != "" {
			t.Fatalf("%s: không được failover hay tính lỗi cho tài khoản: err=%v acc2=%d status=%+v", name, err, acc2.calls, r.Status()[0])
		}
	}
}

// 429 không kèm Retry-After → lỗi trả về vẫn mang Retry-After = cooldown để
// server gửi header cho client.
func TestRouterRetryAfterFromCooldown(t *testing.T) {
	r := NewRouter(Registry{&fake{id: "acc1", prefix: "m-", errs: []error{&UpstreamError{Status: 429, Msg: "429"}}}}, 7*time.Minute)
	_, err := run(t, r, ChatRequest{Model: "m-x"})
	var up *UpstreamError
	if !errors.As(err, &up) || up.RetryAfter != 7*time.Minute {
		t.Fatalf("Retry-After phải = cooldown 7m: %v", err)
	}
}

// Giờ nghỉ chỉ kéo dài; stream bắt đầu trước khi dính 429 thì thành công
// cũng không xoá nghỉ; tài khoản vừa bị cho nghỉ giữa failover thì bỏ qua.
func TestRouterCooldownRaces(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	acc1 := &fake{id: "acc1", prefix: "m-", errs: []error{&UpstreamError{Status: 429, Msg: "429"}}}
	acc2 := &fake{id: "acc2", prefix: "m-", errs: []error{nil}}
	acc3 := &fake{id: "acc3", prefix: "m-", errs: []error{nil}}
	r := NewRouter(Registry{acc1, acc2, acc3}, time.Minute)
	r.now = func() time.Time { return now }

	r.record("acc2", &UpstreamError{Status: 429, RetryAfter: 10 * time.Minute}, now)
	r.record("acc2", &UpstreamError{Status: 429, RetryAfter: time.Minute}, now)
	if got := *r.Status()[1].CoolingUntil; !got.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("429 sau ngắn hơn không được rút ngắn nghỉ: %v", got)
	}
	r.record("acc2", nil, now.Add(-time.Second)) // stream bắt đầu trước 429, xong sau
	if r.Status()[1].CoolingUntil == nil {
		t.Fatal("stream bắt đầu trước 429 không được xoá nghỉ")
	}
	r.record("acc2", nil, now.Add(time.Second)) // bắt đầu sau 429 mà thành công → tài khoản đã chạy lại
	if r.Status()[1].CoolingUntil != nil {
		t.Fatal("lần thử bắt đầu sau 429 thành công phải xoá nghỉ")
	}

	// Plan lập lúc acc2 còn rảnh, rồi acc2 bị request khác cho nghỉ.
	r2 := NewRouter(Registry{acc1, acc2, acc3}, time.Minute)
	r2.now = func() time.Time { return now }
	plan, _ := r2.Plan(ChatRequest{Model: "m-x"})
	r2.record("acc2", &UpstreamError{Status: 429, RetryAfter: time.Minute}, now)
	acc2.calls = 0
	got := ""
	err := r2.Stream(context.Background(), plan, ChatRequest{Model: "m-x"}, func(ev StreamEvent) error { got += ev.Text; return nil })
	if err != nil || got != "acc3" || acc2.calls != 0 {
		t.Fatalf("failover phải bỏ qua acc2 đang nghỉ: got=%q err=%v acc2=%d", got, err, acc2.calls)
	}
}

func TestRouterNotFound(t *testing.T) {
	r := NewRouter(Registry{&fake{id: "acc1", prefix: "m-", errs: []error{nil}}}, time.Minute)
	for _, req := range []ChatRequest{{Model: "khac-x"}, {Model: "m-x", Provider: "khong-co"}} {
		_, err := r.Plan(req)
		var nf *NotFoundError
		if !errors.As(err, &nf) {
			t.Fatalf("%+v phải NotFoundError, nhận %v", req, err)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{
		"":                              0,
		"120":                           2 * time.Minute,
		"-5":                            0,
		"99999999999999999":             0, // tràn int64 khi đổi ra Duration
		"rác":                           0,
		"Fri, 23 Oct 2026 10:01:30 GMT": 90 * time.Second,
	} {
		if got := parseRetryAfter(v, now); got != want {
			t.Fatalf("parseRetryAfter(%q) = %v, muốn %v", v, got, want)
		}
	}
}
