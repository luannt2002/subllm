package tokenstore

// jwt.go — đọc thời hạn (claim "exp") từ JWT của access token. Không kiểm
// chữ ký: chữ ký là việc của server xác thực, subllm chỉ cần biết token
// còn hạn để quyết định refresh.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// jwtExp — trả về thời điểm hết hạn của token JWT.
func jwtExp(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("JWT không đủ 3 đoạn")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode payload JWT: %w", err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("decode claims JWT: %w", err)
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("JWT không có claim exp")
	}
	return time.Unix(claims.Exp, 0), nil
}
