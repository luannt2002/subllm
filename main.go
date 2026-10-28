// subllm — server OpenAI-compatible cục bộ đi qua gói subscription: mỗi
// provider là một tài khoản (file credential của CLI chính thức), route theo
// tiền tố model, nhiều tài khoản cùng tiền tố thì failover khi dính hạn mức.
// Mọi cấu hình nằm ở env/.env, đọc ở config.go (mẫu: .env.example).
//
// Một binary, không dependency ngoài stdlib. Chỉ bind loopback — token là
// chìa khóa tài khoản cá nhân, đừng bao giờ hở ra mạng.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luannt2002/subllm/internal/provider"
	"github.com/luannt2002/subllm/internal/server"
)

func main() {
	envFile := flag.String("env", ".env", "file KEY=VALUE nạp vào env (biến env có sẵn thắng file)")
	flag.Parse()
	explicitEnv := false // -env đặt tường minh mà file không có → lỗi, không im lặng
	flag.Visit(func(f *flag.Flag) { explicitEnv = explicitEnv || f.Name == "env" })

	if err := loadDotEnv(*envFile, explicitEnv); err != nil {
		log.Fatal(err)
	}
	cfg, err := configFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	for _, w := range cfg.Warnings {
		log.Printf("cảnh báo: %s", w)
	}
	ids := make([]string, len(cfg.Providers))
	for i, p := range cfg.Providers {
		ids[i] = p.ID()
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	log.Printf("subllm nghe tại http://%s (provider: %s)", addr, strings.Join(ids, ", "))
	router := provider.NewRouter(cfg.Providers, cfg.Cooldown)
	// ReadHeaderTimeout: client mở kết nối rồi gửi header nhỏ giọt không giữ
	// được goroutine mãi. Không đặt WriteTimeout — sẽ cắt stream dài.
	srv := &http.Server{Addr: addr, Handler: server.New(router, cfg.APIKey), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
