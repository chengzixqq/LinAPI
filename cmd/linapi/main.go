package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"linapi/internal/account"
	"linapi/internal/admin"
	"linapi/internal/billing"
	"linapi/internal/config"
	"linapi/internal/db"
	"linapi/internal/forwarder"
	"linapi/internal/redisx"
	"linapi/internal/routing"
	"linapi/internal/server"
	"linapi/internal/session"
	"linapi/internal/store"

	_ "linapi/internal/adapter/all" // 触发各供应商适配器 init() 注册

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 结构化日志器：按配置选级别与格式（json/text），设为全局默认，
	// 供未显式注入 logger 的组件（slog.Default()）复用。
	logger := buildLogger(cfg.Log)
	slog.SetDefault(logger)

	// Redis：限流等分布式状态的强依赖，连不上直接退出。
	rdb, err := redisx.New(cfg.Redis)
	if err != nil {
		log.Fatalf("初始化 Redis 失败: %v", err)
	}
	defer func() { _ = rdb.Close() }()

	// 数据访问层：DB 启用则用 PostgreSQL（sqlc 查询），否则回退配置驱动的内存实现。
	dl := buildDataLayer(cfg)
	if dl.pool != nil {
		defer dl.pool.Close()
	}

	// 计费门面：Redis 原子预扣/退差 + 用量日志异步落库。
	// recorder 单独持有，优雅关闭时冲刷残留日志。sink 由数据层决定
	// （PG 启用时为 PGSink 批量落库，否则 NopSink 丢弃）。
	bill, recorder := buildBilling(cfg.Billing, rdb, dl.sink)
	defer recorder.Close()

	// 路由引擎 + 转发层：把适配器 + 路由 + 熔断 + 计费串起来真正发 HTTP。
	router := routing.NewRouter(dl.channels, routing.DefaultBreakerConfig())
	log.Printf("路由引擎已加载 %d 个渠道", len(dl.channels))
	fwd := forwarder.New(router, bill, logger)

	// 管理面服务：用户/密钥/渠道 CRUD，渠道写操作触发 router 热更新。
	adminSvc := admin.NewService(dl.adminStore, router, logger)

	// 会话管理器：控制台登录态载体（Redis）。
	sessions := session.NewManager(rdb)

	// 播种首个管理员账户（仅当配置了 bootstrap 且该用户名不存在时）。
	bootstrapAdmin(cfg, dl.account, logger)

	srv := server.New(cfg, server.Deps{
		Store:        dl.store,
		Redis:        rdb,
		Billing:      bill,
		Forwarder:    fwd,
		Admin:        adminSvc,
		Account:      dl.account,
		Settings:     dl.settings,
		Session:      sessions,
		SecureCookie: cfg.Server.Mode == "release", // 生产（release）走 HTTPS，加 Secure。
		Logger:       logger,
	})

	// 渠道定时热重载：多实例部署时，本实例通过定期从存储重载收敛其它实例的渠道变更。
	// 仅 database.enabled=true 且间隔 >0 时启用（内存模式无共享存储，重载无意义）。
	stopReload := startChannelReload(cfg, dl.pool != nil, adminSvc)
	defer stopReload()

	// 在独立 goroutine 中启动，以便主协程监听退出信号。
	go func() {
		log.Printf("LinAPI 网关已启动，监听 %s", srv.Addr())
		if err := srv.Start(); err != nil {
			log.Fatalf("服务器异常退出: %v", err)
		}
	}()

	// 等待 SIGINT / SIGTERM，触发优雅关闭。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("收到退出信号，正在优雅关闭...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("关闭超时，强制退出: %v", err)
	}

	log.Println("已安全退出")
}

// buildLogger 按配置构建结构化日志器：level 决定最低级别，format 决定
// 输出编码（json 便于机器采集，text 便于本地阅读），统一写到 stdout。
func buildLogger(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.ToLower(cfg.Format) == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

// buildBilling 用配置构建计费门面，并返回其异步用量记录器（供优雅关闭时冲刷）。
// sink 决定用量日志的落库目的地（PGSink 或 NopSink），由 buildDataLayer 选定。
func buildBilling(cfg config.BillingConfig, rdb *redis.Client, sink billing.Sink) (*billing.Billing, *billing.Recorder) {
	models := make(map[string]billing.ModelPrice, len(cfg.Models))
	for _, m := range cfg.Models {
		models[m.Model] = billing.ModelPrice{
			InputPer1M:  m.InputPer1M,
			OutputPer1M: m.OutputPer1M,
		}
	}
	pricing := billing.NewPricing(models, cfg.DefaultInputPer1M, cfg.DefaultOutputPer1M)
	account := billing.NewAccount(rdb)
	recorder := billing.NewRecorder(sink, billing.RecorderConfig{}, nil)

	return billing.New(pricing, account, recorder, cfg.DefaultReserve), recorder
}

// dataLayer 聚合数据访问层的装配产物。
type dataLayer struct {
	store      store.Store           // 身份/额度数据访问（热路径）
	adminStore admin.AdminStore      // 管理面数据访问（用户/密钥/渠道 CRUD）
	account    account.AccountStore  // 控制台账户数据访问
	settings   account.SettingsStore // 系统设置数据访问
	sink       billing.Sink          // 用量日志落库目的地
	channels   []*routing.Channel    // 路由引擎初始渠道
	pool       *pgxpool.Pool         // 非 nil 表示 PG 生效，随进程退出关闭
}

// buildDataLayer 装配数据访问层。
//
// 决策：database.enabled=true 时连 PostgreSQL——建池、（可选）自动建表、
// 用 sqlc 查询装配 PGStore + PGSink + PG AdminStore，并从 channels 表加载启用渠道。
// 连库失败视为致命（显式开启却连不上应尽早暴露）。
// database.enabled=false 时回退配置驱动的内存实现：AdminStore 复用同一 MemoryStore
// 实例（使管理面写入即时对热路径可见）+ NopSink + config 渠道（本地开发免装 DB）。
// pool 为 nil 表示走内存分支，调用方无需关闭。
func buildDataLayer(cfg *config.Config) dataLayer {
	if !cfg.Database.Enabled {
		log.Println("数据库未启用（database.enabled=false），使用内存 Store + 丢弃用量日志")
		mem := store.NewMemoryStore(buildKeySeeds(cfg.Auth))
		adminChannels := configToAdminChannels(cfg.Channels)
		accStore := account.NewMemoryStore(mem)
		return dataLayer{
			store:      mem,
			adminStore: admin.NewMemoryStore(mem, adminChannels),
			account:    accStore,
			settings:   accStore,
			sink:       billing.NopSink{},
			channels:   forwarder.ChannelsFromConfig(cfg.Channels),
			pool:       nil,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.NewPool(ctx, db.PoolConfig{
		DSN:             cfg.Database.DSN,
		MaxConns:        int32(cfg.Database.MaxOpenConns),
		MinConns:        int32(cfg.Database.MaxIdleConns),
		ConnMaxLifetime: time.Duration(cfg.Database.ConnMaxLifetime) * time.Second,
	})
	if err != nil {
		log.Fatalf("数据库已启用但连接失败: %v", err)
	}

	if cfg.Database.AutoMigrate {
		if err := db.ApplySchema(ctx, pool); err != nil {
			pool.Close()
			log.Fatalf("应用数据库 schema 失败: %v", err)
		}
		log.Println("数据库 schema 已应用（auto_migrate=true）")
	}

	q := db.New(pool)

	// 从 channels 表加载启用渠道喂给路由引擎。
	rows, err := q.ListEnabledChannels(ctx)
	if err != nil {
		pool.Close()
		log.Fatalf("加载渠道失败: %v", err)
	}
	channels, err := forwarder.ChannelsFromDB(rows)
	if err != nil {
		pool.Close()
		log.Fatalf("解析渠道配置失败: %v", err)
	}

	log.Println("数据库已启用，使用 PostgreSQL Store + 用量日志落库")
	accStore := account.NewPGStore(pool)
	return dataLayer{
		store:      store.NewPGStore(q),
		adminStore: admin.NewPGStore(q),
		account:    accStore,
		settings:   accStore,
		sink:       billing.NewPGSink(q),
		channels:   channels,
		pool:       pool,
	}
}

// configToAdminChannels 把配置渠道转换为管理面渠道视图（内存模式的渠道初始集）。
func configToAdminChannels(cfgs []config.ChannelConfig) []admin.Channel {
	out := make([]admin.Channel, 0, len(cfgs))
	for _, c := range cfgs {
		models := c.Models
		if models == nil {
			models = map[string]string{}
		}
		out = append(out, admin.Channel{
			ChannelID: c.ID,
			Name:      c.Name,
			Format:    c.Format,
			BaseURL:   c.BaseURL,
			APIKey:    c.APIKey,
			Models:    models,
			Priority:  c.Priority,
			Weight:    c.Weight,
			Enabled:   c.Enabled,
		})
	}
	return out
}

// buildKeySeeds 把配置中的预置密钥转换为内存 Store 的种子。
func buildKeySeeds(auth config.AuthConfig) []store.KeySeed {
	seeds := make([]store.KeySeed, 0, len(auth.Keys))
	for _, k := range auth.Keys {
		seeds = append(seeds, store.KeySeed{
			APIKey:          k.APIKey,
			KeyID:           k.KeyID,
			UserID:          k.UserID,
			RateLimitPerMin: k.RateLimitPerMin,
			AllowedModels:   k.AllowedModels,
			Enabled:         k.Enabled,
			InitialBalance:  k.InitialBalance,
		})
	}
	return seeds
}

// startChannelReload 启动渠道定时热重载 goroutine，返回停止函数（幂等）。
//
// 仅在 dbEnabled 且 admin.channel_reload_interval>0 时真正启动——内存模式没有
// 共享存储，定时重载只会把本进程内存态原样写回，无意义。返回的停止函数在优雅
// 关闭时调用，确保 goroutine 退出。
func startChannelReload(cfg *config.Config, dbEnabled bool, svc *admin.Service) func() {
	interval := cfg.Admin.ChannelReloadInterval
	if !dbEnabled || interval <= 0 {
		return func() {}
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := svc.ReloadChannels(ctx); err != nil {
					log.Printf("渠道定时热重载失败: %v", err)
				}
			}
		}
	}()
	log.Printf("渠道定时热重载已启用，间隔 %ds", interval)
	return cancel
}

// bootstrapAdmin 在配置了 admin.bootstrap 且该用户名尚不存在时，播种首个管理员账户。
// 幂等：已存在同名账户则跳过。密码为空时告警并跳过（绝不建空密码账户）。
func bootstrapAdmin(cfg *config.Config, accounts account.AccountStore, logger *slog.Logger) {
	bs := cfg.Admin.Bootstrap
	if !cfg.Admin.Enabled || bs.Username == "" {
		return
	}
	if bs.Password == "" {
		logger.Warn("跳过管理员播种：admin.bootstrap.username 已设但密码为空",
			"username", bs.Username)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := accounts.GetByUsername(ctx, bs.Username); err == nil {
		logger.Info("管理员账户已存在，跳过播种", "username", bs.Username)
		return
	}
	hash, err := account.HashPassword(bs.Password)
	if err != nil {
		logger.Error("管理员播种失败：密码不合规", "err", err)
		return
	}
	if _, err := accounts.CreateAccount(ctx, account.CreateAccountInput{
		Username: bs.Username, PasswordHash: hash, Role: account.RoleAdmin,
	}); err != nil {
		logger.Error("管理员播种失败", "username", bs.Username, "err", err)
		return
	}
	logger.Info("已播种首个管理员账户", "username", bs.Username)
}
