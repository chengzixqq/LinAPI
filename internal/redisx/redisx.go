// Package redisx 封装共享的 Redis 客户端。
//
// 网关的限流、额度、计费等模块都依赖 Redis 做原子操作与分布式状态，
// 这里统一从配置构建客户端、做连通性探测，并向上层暴露一个 *redis.Client。
package redisx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"time"

	"linapi/internal/config"

	"github.com/redis/go-redis/v9"
)

// New 按配置构建 Redis 客户端，并用 PING 做一次连通性探测。
// 探测失败即返回错误——Redis 是限流/额度的强依赖，启动阶段就应暴露问题。
func New(cfg config.RedisConfig) (*redis.Client, error) {
	tlsConfig, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(&redis.Options{
		Addr:      cfg.Addr,
		Username:  cfg.Username,
		Password:  cfg.Password,
		DB:        cfg.DB,
		TLSConfig: tlsConfig,
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

// ValidateSecurity 在 release 启动前阻止远程明文 Redis。loopback 可用于同机部署；
// 其它地址必须启用 TLS，或由运维显式声明已有可信隧道。
func ValidateSecurity(cfg config.RedisConfig, release bool) error {
	if !release || cfg.TLS.Enabled || cfg.AllowInsecureRemote {
		return nil
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return fmt.Errorf("redisx: 非法地址 %q: %w", cfg.Addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("redisx: release 模式远程 Redis %q 必须启用 TLS", cfg.Addr)
}

func buildTLSConfig(cfg config.RedisConfig) (*tls.Config, error) {
	if !cfg.TLS.Enabled {
		return nil, nil
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.TLS.ServerName}
	if tlsCfg.ServerName == "" {
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil {
			return nil, fmt.Errorf("redisx: TLS 地址无效: %w", err)
		}
		tlsCfg.ServerName = host
	}
	if cfg.TLS.CAFile != "" {
		pem, err := os.ReadFile(cfg.TLS.CAFile)
		if err != nil {
			return nil, fmt.Errorf("redisx: 读取 CA 失败: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("redisx: CA 文件不含有效证书")
		}
		tlsCfg.RootCAs = roots
	}
	if (cfg.TLS.CertFile == "") != (cfg.TLS.KeyFile == "") {
		return nil, fmt.Errorf("redisx: cert_file 与 key_file 必须同时配置")
	}
	if cfg.TLS.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("redisx: 读取客户端证书失败: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return tlsCfg, nil
}
