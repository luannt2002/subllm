package provider

// router.go — chọn provider cho request và failover giữa nhiều tài khoản.
//
// Luật:
//   - field "provider" đích danh → đúng provider đó, không failover, bỏ qua
//     thời gian nghỉ (người gọi đã chọn).
//   - còn lại → mọi provider khớp tiền tố model, theo thứ tự SUBLLM_PROVIDERS,
//     bỏ những cái đang nghỉ. Lỗi tài khoản (credential, 401, 403) hoặc 429
//     → thử cái kế, NHƯNG chỉ khi chưa phát sự kiện nào cho client (stream
//     đã chảy dở thì không đổi tài khoản giữa câu được).
//   - 429 → provider nghỉ theo Retry-After, không có thì theo cooldown. Mọi
//     provider khớp đều đang nghỉ → trả 429 ngay, không gọi upstream.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// Router — Registry + trạng thái từng tài khoản.
type Router struct {
	providers Registry
	cooldown  time.Duration
	now       func() time.Time

	mu    sync.Mutex
	state map[string]*account
}

// account — trạng thái trong bộ nhớ của một provider.
type account struct {
	ok, failed, failovers int64
	lastError             string
	lastErrorAt           time.Time
	coolingUntil          time.Time
}

// AccountStatus — số đếm của một provider, phục vụ GET /status: failover
// che lỗi khỏi client nên phải đếm được thì mới thấy tài khoản chết.
type AccountStatus struct {
	Name         string     `json:"name"`
	OK           int64      `json:"ok"`
	Failed       int64      `json:"failed"`
	Failovers    int64      `json:"failovers"`
	LastError    string     `json:"last_error,omitempty"`
	LastErrorAt  *time.Time `json:"last_error_at,omitempty"`
	CoolingUntil *time.Time `json:"cooling_until,omitempty"` // nil = không nghỉ
}

// NewRouter — cooldown là thời gian nghỉ sau 429 khi upstream không gửi
// Retry-After.
func NewRouter(providers Registry, cooldown time.Duration) *Router {
	r := &Router{providers: providers, cooldown: cooldown, now: time.Now, state: map[string]*account{}}
	for _, p := range providers {
		r.state[p.ID()] = &account{}
	}
	return r
}

// Plan — các provider sẽ thử cho req, theo thứ tự.
func (r *Router) Plan(req ChatRequest) ([]Provider, error) {
	if req.Provider != "" {
		p, err := r.providers.ByID(req.Provider)
		if err != nil {
			return nil, &NotFoundError{Msg: err.Error()}
		}
		return []Provider{p}, nil
	}
	var ready []Provider
	var earliest time.Time
	matched := false
	r.mu.Lock()
	now := r.now()
	for _, p := range r.providers {
		if !p.Match(req.Model) {
			continue
		}
		matched = true
		until := r.state[p.ID()].coolingUntil
		if until.After(now) {
			if earliest.IsZero() || until.Before(earliest) {
				earliest = until
			}
			continue
		}
		ready = append(ready, p)
	}
	r.mu.Unlock()
	if !matched {
		_, err := r.providers.Route(req.Model)
		return nil, &NotFoundError{Msg: err.Error()}
	}
	if len(ready) == 0 {
		wait := earliest.Sub(now)
		return nil, &UpstreamError{
			Status: http.StatusTooManyRequests, RetryAfter: wait,
			Msg: fmt.Sprintf("mọi provider nhận model %q đang nghỉ sau 429, thử lại sau %s", req.Model, wait.Round(time.Second)),
		}
	}
	return ready, nil
}

// Stream — chạy plan, failover sang provider kế khi được phép. Lỗi trả về
// mang tên provider cuối cùng đã thử.
func (r *Router) Stream(ctx context.Context, plan []Provider, req ChatRequest, emit func(StreamEvent) error) error {
	var lastErr, prevErr error
	var prev Provider
	for i, p := range plan {
		if i > 0 && r.cooling(p.ID()) {
			continue // vừa bị request khác cho nghỉ trong lúc ta đang thử cái trước
		}
		if prev != nil {
			r.bump(prev.ID())
			log.Printf("failover %s → %s: %v", prev.ID(), p.ID(), prevErr)
		}
		begin := r.now()
		started := false
		err := p.StreamChat(ctx, req, func(ev StreamEvent) error {
			started = true
			return emit(ev)
		})
		var up *UpstreamError
		if errors.As(err, &up) && up.Status == http.StatusTooManyRequests && up.RetryAfter <= 0 {
			up.RetryAfter = r.cooldown // client cũng nhận Retry-After dù upstream không gửi
		}
		if ctx.Err() == nil && !ClientFault(err) {
			r.record(p.ID(), err, begin) // client bỏ đi / request sai không phải lỗi của tài khoản
		}
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("%s: %w", p.ID(), err)
		if started || !failoverable(err) || ctx.Err() != nil {
			return lastErr
		}
		prev, prevErr = p, err
	}
	return lastErr
}

func (r *Router) cooling(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state[id].coolingUntil.After(r.now())
}

// failoverable — lỗi của riêng tài khoản, tài khoản khác có thể chạy.
func failoverable(err error) bool {
	var acc *AccountError
	if errors.As(err, &acc) {
		return true
	}
	var up *UpstreamError
	if errors.As(err, &up) {
		switch up.Status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			return true
		}
	}
	return false
}

// record — ghi kết quả một lần thử bắt đầu lúc begin.
func (r *Router) record(id string, err error, begin time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.state[id]
	now := r.now()
	if err == nil {
		st.ok++
		// Stream dài bắt đầu trước khi tài khoản dính 429 thì không xoá nghỉ.
		if st.lastErrorAt.Before(begin) {
			st.coolingUntil = time.Time{}
		}
		return
	}
	st.failed++
	st.lastError = Truncate([]byte(err.Error()), ErrSnippet)
	st.lastErrorAt = now
	var up *UpstreamError
	if errors.As(err, &up) && up.Status == http.StatusTooManyRequests {
		if until := now.Add(up.RetryAfter); until.After(st.coolingUntil) {
			st.coolingUntil = until // nhiều 429 cùng lúc: chỉ kéo dài, không rút ngắn
		}
	}
}

func (r *Router) bump(id string) {
	r.mu.Lock()
	r.state[id].failovers++
	r.mu.Unlock()
}

// Status — bản chụp số đếm, theo thứ tự provider.
func (r *Router) Status() []AccountStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := make([]AccountStatus, 0, len(r.providers))
	for _, p := range r.providers {
		st := r.state[p.ID()]
		s := AccountStatus{Name: p.ID(), OK: st.ok, Failed: st.failed, Failovers: st.failovers, LastError: st.lastError}
		if !st.lastErrorAt.IsZero() {
			t := st.lastErrorAt
			s.LastErrorAt = &t
		}
		if st.coolingUntil.After(now) {
			t := st.coolingUntil
			s.CoolingUntil = &t
		}
		out = append(out, s)
	}
	return out
}

// Providers — danh sách provider (cho GET /v1/models).
func (r *Router) Providers() Registry { return r.providers }
