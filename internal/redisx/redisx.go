// Package redisx 封装共享的 Redis 客户端。
//
// 网关的限流、额度、计费等模块都依赖 Redis 做原子操作与分布式状态，
// 这里统一从配置构建客户端、做连通性探测，并向上层暴露一个 *redis.Client。
package redisx

import (
	"context"
	"fmt"
	"time"

	"linapi/internal/config"

	"github.com/redis/go-redis/v9"
)

// New 按配置构建 Redis 客户端，并用 PING 做一次连通性探测。
// 探测失败即返回错误——Redis 是限流/额度的强依赖，启动阶段就应暴露问题。
func New(cfg config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		// 关闭以释放刚建立的连接，避免泄漏。
		_ = client.Close()
		return nil, fmt.Errorf("redisx: 连接 Redis(%s) 失败: %w", cfg.Addr, err)
	}

	return client, nil
}
