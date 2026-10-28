package provider

// errors.go — lỗi có kiểu để Router quyết định failover sang tài khoản khác.

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"
)

// UpstreamError — upstream trả HTTP khác 200 trước khi stream bắt đầu.
type UpstreamError struct {
	Status     int
	RetryAfter time.Duration // 0 nếu upstream không gửi Retry-After
	Msg        string
}

func (e *UpstreamError) Error() string { return e.Msg }

// AccountError — lỗi của riêng một tài khoản (file credential, refresh bị
// từ chối…): tài khoản khác cùng kiểu vẫn có thể chạy.
type AccountError struct{ Err error }

func (e *AccountError) Error() string { return e.Err.Error() }
func (e *AccountError) Unwrap() error { return e.Err }

// RequestError — request của client không dịch được sang backend (vd role
// lạ): tài khoản nào cũng sẽ lỗi như nhau, không failover, không tính lỗi
// cho tài khoản, trả 400.
type RequestError struct{ Err error }

func (e *RequestError) Error() string { return e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

// ClientFault — lỗi do chính request của client (không dịch được, hoặc
// upstream trả 400): tài khoản nào cũng sẽ lỗi như nhau → không failover,
// không tính lỗi cho tài khoản, client nhận 400 (SDK không tự thử lại 4xx).
func ClientFault(err error) bool {
	var re *RequestError
	if errors.As(err, &re) {
		return true
	}
	var up *UpstreamError
	return errors.As(err, &up) && up.Status == http.StatusBadRequest
}

// NotFoundError — không provider nào nhận model / tên provider.
type NotFoundError struct{ Msg string }

func (e *NotFoundError) Error() string { return e.Msg }

// parseRetryAfter — header Retry-After dạng giây hoặc HTTP-date; lạ → 0.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseInt(v, 10, 64); err == nil && s > 0 && s <= math.MaxInt64/int64(time.Second) {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// retryAfterFromBody — Codex báo giờ reset hạn mức trong body 429
// (error.resets_in_seconds), không qua header Retry-After; không có → 0.
func retryAfterFromBody(raw []byte) time.Duration {
	var b struct {
		Error struct {
			ResetsInSeconds int64 `json:"resets_in_seconds"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return 0
	}
	s := b.Error.ResetsInSeconds
	if s <= 0 || s > math.MaxInt64/int64(time.Second) {
		return 0
	}
	return time.Duration(s) * time.Second
}
