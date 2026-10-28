package tokenstore

import (
	"testing"
	"time"
)

func TestJWTExp(t *testing.T) {
	want := time.Unix(1792747926, 0)
	got, err := jwtExp(jwtAt(want))
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
