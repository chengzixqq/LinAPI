// Command devredis 启动一个进程内的 miniredis，并通过真实 TCP 暴露，
// 供本地开发时替代外部 Redis——网关的限流/会话/CSRF 都依赖 Redis，
// 但本机未必装了 redis-server。这是**仅供开发**的辅助程序，绝不用于生产。
//
// 用法：
//
//	go run ./cmd/devredis                 # 监听 127.0.0.1:6379
//	go run ./cmd/devredis -addr :6380     # 自定义地址
//
// 进程持续运行直到收到 Ctrl+C / SIGTERM；期间数据只在内存，退出即丢。
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/alicebob/miniredis/v2"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:6379", "miniredis 监听地址")
	flag.Parse()

	mr := miniredis.NewMiniRedis()
	if err := mr.StartAddr(*addr); err != nil {
		log.Fatalf("devredis: 启动 miniredis 失败（端口是否被占用？）: %v", err)
	}
	defer mr.Close()
	log.Printf("devredis: 进程内 miniredis 已监听 %s（仅供开发，数据不持久）", mr.Addr())

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("devredis: 收到退出信号，关闭 miniredis")
}
