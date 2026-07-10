package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	channelKeys, err := channelKeyCipherForConfig(cfg)
	if err != nil {
		log.Fatalf("PostgreSQL 渠道密钥加密配置无效: %v", err)
	}
	if cfg.Server.Mode == "release" && !cfg.Database.Enabled {
		log.Fatal("release 模式必须启用 PostgreSQL 持久账本（database.enabled=true）")
	}
	if cfg.Server.Mode == "release" && (cfg.Server.ReadTimeoutSeconds <= 0 ||
		cfg.Server.IdleTimeoutSeconds <= 0 || cfg.Server.MaxRequestBodyBytes <= 0 || cfg.Server.MaxHeaderBytes <= 0) {
		log.Fatal("release 模式必须配置非零 server.read_timeout_seconds、idle_timeout_seconds、max_request_body_bytes 与 max_header_bytes")
	}
	if cfg.Server.Mode == "release" && cfg.Server.MetricsToken == "" {
		log.Fatal("release 模式必须通过 server.metrics_token 保护 /metrics")
	}
	if cfg.Server.Mode == "release" &&
		(cfg.Server.MetricsMaxRequestsInFlight <= 0 || cfg.Server.MetricsTimeoutSeconds <= 0) {
		log.Fatal("release 模式必须配置正数 server.metrics_max_requests_in_flight 与 metrics_timeout_seconds")
	}
	if cfg.Server.Mode == "release" &&
		(cfg.Auth.UnauthenticatedRateLimitPerMin <= 0 || cfg.Auth.AccountRateLimitPerMin <= 0) {
		log.Fatal("release 模式必须配置正数 auth.unauthenticated_rate_limit_per_min 与 account_rate_limit_per_min")
	}
	if cfg.Server.Mode == "release" && cfg.Admin.Enabled &&
		(cfg.Admin.AuthRateLimitPerMin <= 0 || cfg.Admin.AuthIdentifierRateLimitPerMin <= 0 ||
			cfg.Admin.MaxActiveSessionsPerAccount <= 0) {
		log.Fatal("release 管理面必须配置正数 admin.auth_rate_limit_per_min、auth_identifier_rate_limit_per_min 与 max_active_sessions_per_account")
	}
	if cfg.Database.MinIdleConns < 0 || cfg.Database.MaxOpenConns <= 0 ||
		cfg.Database.MinIdleConns > cfg.Database.MaxOpenConns {
		log.Fatal("database.min_idle_conns 必须在 0 到 max_open_conns 之间")
	}

	// 结构化日志器：按配置选级别与格式（json/text），设为全局默认，
	// 供未显式注入 logger 的组件（slog.Default()）复用。
	logger := buildLogger(cfg.Log)
	slog.SetDefault(logger)

	// Redis：限流等分布式状态的强依赖，连不上直接退出。
	if err := redisx.ValidateSecurity(cfg.Redis, cfg.Server.Mode == "release"); err != nil {
		log.Fatalf("Redis 安全配置无效: %v", err)
	}
	rdb, err := redisx.New(cfg.Redis)
	if err != nil {
		log.Fatalf("初始化 Redis 失败: %v", err)
	}
	defer func() { _ = rdb.Close() }()

	// 数据访问层：DB 启用则用 PostgreSQL（sqlc 查询），否则回退配置驱动的内存实现。
	dl := buildDataLayer(cfg, channelKeys)
	if dl.pool != nil {
		defer dl.pool.Close()
	}

	// 计费门面：PG/内存 Ledger 持久预授权 + 幂等结算。Redis 只承担限流与会话，
	// 不再参与任何资金增量修改。
	bill := buildBilling(cfg.Billing, dl.ledger, cfg.Server.Mode == "release")
	if cfg.Server.Mode == "release" {
		seenModels := make(map[string]struct{})
		for _, ch := range dl.channels {
			for model := range ch.Models {
				seenModels[model] = struct{}{}
			}
		}
		for model := range seenModels {
			if err := bill.ValidateModel(model); err != nil {
				log.Fatalf("模型 %q 缺少安全计费策略: %v", model, err)
			}
		}
	}
	recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := bill.Recover(recoverCtx); err != nil {
		if errors.Is(err, billing.ErrAmbiguousReservations) {
			log.Printf("警告：检测到发送结果不确定的历史预授权，已保持冻结等待人工对账: %v", err)
		} else {
			recoverCancel()
			log.Fatalf("恢复未完成计费结算失败: %v", err)
		}
	}
	recoverCancel()
	stopBillingRecovery := startBillingRecovery(bill, logger)
	defer stopBillingRecovery()

	// 路由引擎 + 转发层：把适配器 + 路由 + 熔断 + 计费串起来真正发 HTTP。
	outputLimits, err := forwarder.NewOpenAIOutputLimitResolver(
		cfg.Billing.OpenAIOutputLimitFields,
		cfg.Server.Mode == "release",
	)
	if err != nil {
		log.Fatalf("OpenAI 输出上限字段策略无效: %v", err)
	}
	if cfg.Server.Mode == "release" {
		if err := outputLimits.ValidateChannels(dl.channels); err != nil {
			log.Fatalf("OpenAI 输出上限字段策略不完整: %v", err)
		}
	}
	targetPolicy, err := forwarder.NewUpstreamTargetPolicy(cfg.Upstream, cfg.Server.Mode == "release")
	if err != nil {
		log.Fatalf("上游目标策略无效: %v", err)
	}
	if err := targetPolicy.ValidateChannels(dl.channels); err != nil {
		log.Fatalf("上游渠道目标不安全: %v", err)
	}
	validateChannel := func(ch *routing.Channel) error {
		if err := targetPolicy.ValidateChannel(ch); err != nil {
			return err
		}
		return outputLimits.ValidateChannel(ch)
	}
	router := routing.NewRouter(dl.channels, routing.DefaultBreakerConfig())
	log.Printf("路由引擎已加载 %d 个渠道", len(dl.channels))
	fwd := forwarder.NewWithPolicies(router, bill, outputLimits, targetPolicy, logger)

	// 管理面服务：用户/密钥/渠道 CRUD，渠道写操作触发 router 热更新。
	adminSvc := admin.NewServiceWithChannelValidator(dl.adminStore, router, logger, validateChannel)

	// 会话管理器：控制台登录态载体（Redis）。
	sessions := session.NewManagerWithLimit(rdb, cfg.Admin.MaxActiveSessionsPerAccount)

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
		Ready: func(ctx context.Context) error {
			if err := rdb.Ping(ctx).Err(); err != nil {
				return fmt.Errorf("Redis 未就绪: %w", err)
			}
			if dl.pool != nil {
				if err := dl.pool.Ping(ctx); err != nil {
					return fmt.Errorf("PostgreSQL 未就绪: %w", err)
				}
			}
			return nil
		},
	})

	// 渠道定时热重载：多实例部署时，本实例通过定期从存储重载收敛其它实例的渠道变更。
	// 仅 database.enabled=true 且间隔 >0 时启用（内存模式无共享存储，重载无意义）。
	stopReload := startChannelReload(cfg, dl.pool != nil, adminSvc)
	defer stopReload()

	// 在独立 goroutine 中启动，并把错误交回主协程；不能在 goroutine 内
	// log.Fatal/os.Exit，否则 PostgreSQL、Redis 与恢复任务的 defer 全被跳过。
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("LinAPI 网关已启动，监听 %s", srv.Addr())
		serverErr <- srv.Start()
	}()

	// 等待 SIGINT / SIGTERM 或启动/运行错误。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serverErr:
		log.Printf("服务器异常退出: %v", err)
		return
	case <-quit:
	}

	log.Println("收到退出信号，正在优雅关闭...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		// 返回 main 仍会执行全部 defer；由外部进程管理器按日志/健康检查拉起。
		log.Printf("关闭超时: %v", err)
		return
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

// buildBilling 用价格与模型计费上界构建持久计费门面。
func buildBilling(cfg config.BillingConfig, ledger billing.Ledger, strict bool) *billing.Billing {
	models := make(map[string]billing.ModelPolicy, len(cfg.Models))
	for _, m := range cfg.Models {
		models[m.Model] = billing.ModelPolicy{
			ModelPrice: billing.ModelPrice{
				InputPer1M: m.InputPer1M, OutputPer1M: m.OutputPer1M,
				CacheCreationInputPer1M: m.CacheCreationInputPer1M,
				CacheReadInputPer1M:     m.CacheReadInputPer1M,
			},
			MaxBillableInputTokens: m.MaxBillableInputTokens,
			MaxOutputTokens:        m.MaxOutputTokens,
		}
	}
	fallback := billing.ModelPolicy{
		ModelPrice: billing.ModelPrice{
			InputPer1M: cfg.DefaultInputPer1M, OutputPer1M: cfg.DefaultOutputPer1M,
			CacheCreationInputPer1M: cfg.DefaultCacheCreationInputPer1M,
			CacheReadInputPer1M:     cfg.DefaultCacheReadInputPer1M,
		},
		MaxBillableInputTokens: cfg.DefaultMaxBillableInputTokens,
		MaxOutputTokens:        cfg.DefaultMaxOutputTokens,
	}
	pricing := billing.NewPricingWithPolicies(models, fallback)
	if strict {
		pricing = billing.NewStrictPricingWithPolicies(models, fallback)
	}
	return billing.New(pricing, ledger, cfg.DefaultReserve)
}

// startBillingRecovery 周期重试 consumed_unsettled，避免一次瞬时 PG 故障把用户
// 预授权一直冻结到下次进程重启。过期 in_flight 只告警并等待人工对账。
func startBillingRecovery(bill *billing.Billing, logger *slog.Logger) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCtx, runCancel := context.WithTimeout(ctx, 10*time.Second)
				err := bill.Recover(runCtx)
				runCancel()
				if err == nil {
					continue
				}
				if errors.Is(err, billing.ErrAmbiguousReservations) {
					logger.Warn("检测到过期 in_flight 预授权，需人工向对应 channel 对账", "err", err)
				} else {
					logger.Error("周期恢复未完成计费结算失败", "err", err)
				}
			}
		}
	}()
	return cancel
}

// dataLayer 聚合数据访问层的装配产物。
type dataLayer struct {
	store      store.Store           // 身份/额度数据访问（热路径）
	adminStore admin.AdminStore      // 管理面数据访问（用户/密钥/渠道 CRUD）
	account    account.AccountStore  // 控制台账户数据访问
	settings   account.SettingsStore // 系统设置数据访问
	ledger     billing.Ledger        // 权威预授权/结算账本
	channels   []*routing.Channel    // 路由引擎初始渠道
	pool       *pgxpool.Pool         // 非 nil 表示 PG 生效，随进程退出关闭
}

// buildDataLayer 装配数据访问层。
//
// 决策：database.enabled=true 时连 PostgreSQL——建池、（可选）自动建表、
// 用 sqlc 查询装配 PGStore + PGLedger + PG AdminStore，并从 channels 表加载启用渠道。
// 连库失败视为致命（显式开启却连不上应尽早暴露）。
// database.enabled=false 时回退配置驱动的内存实现：AdminStore 复用同一 MemoryStore
// 实例（使管理面写入即时对热路径可见）+ MemoryLedger + config 渠道。
// pool 为 nil 表示走内存分支，调用方无需关闭。
func buildDataLayer(cfg *config.Config, channelKeys *admin.ChannelKeyCipher) dataLayer {
	if !cfg.Database.Enabled {
		log.Println("数据库未启用（database.enabled=false），使用仅供开发的内存 Store + MemoryLedger")
		mem := store.NewMemoryStore(buildKeySeeds(cfg.Auth))
		adminChannels := configToAdminChannels(cfg.Channels)
		accStore := account.NewMemoryStore(mem)
		return dataLayer{
			store:      mem,
			adminStore: admin.NewMemoryStore(mem, adminChannels),
			account:    accStore,
			settings:   accStore,
			ledger:     billing.NewMemoryLedger(mem),
			channels:   forwarder.ChannelsFromConfig(cfg.Channels),
			pool:       nil,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.NewPool(ctx, db.PoolConfig{
		DSN:             cfg.Database.DSN,
		MaxConns:        int32(cfg.Database.MaxOpenConns),
		MinConns:        int32(cfg.Database.MinIdleConns),
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
		log.Println("数据库版本化迁移已应用（auto_migrate=true）")
	} else if err := db.VerifySchema(ctx, pool); err != nil {
		pool.Close()
		log.Fatalf("数据库 schema 版本校验失败: %v", err)
	}

	q := db.New(pool)
	migrated, err := admin.MigrateChannelAPIKeys(
		ctx,
		pool,
		channelKeys,
		cfg.Database.ChannelKeyEncryption.MigratePlaintext,
	)
	if err != nil {
		pool.Close()
		log.Fatalf("验证或迁移 PostgreSQL 渠道密钥失败: %v", err)
	}
	if migrated > 0 {
		log.Printf("已在单个事务中加密迁移 %d 个历史渠道密钥；请立即关闭 migrate_plaintext", migrated)
	}
	if cfg.Database.AutoMigrate {
		if err := db.ValidateChannelKeyEnvelopeConstraint(ctx, pool); err != nil {
			pool.Close()
			log.Fatalf("渠道密钥密文约束验证失败: %v", err)
		}
	}
	adminStore := admin.NewPGStore(q, channelKeys)

	// 通过 PGStore 解密后再送入路由；SQL 原始密文不能越过此数据访问边界。
	storedChannels, err := adminStore.ListChannels(ctx)
	if err != nil {
		pool.Close()
		log.Fatalf("加载渠道失败: %v", err)
	}
	channels := make([]*routing.Channel, 0, len(storedChannels))
	for _, ch := range admin.ChannelsToRouting(storedChannels) {
		if ch.Enabled {
			channels = append(channels, ch)
		}
	}

	log.Println("数据库已启用，使用 PostgreSQL Store + 持久预授权账本")
	accStore := account.NewPGStore(pool)
	return dataLayer{
		store:      store.NewPGStore(q),
		adminStore: adminStore,
		account:    accStore,
		settings:   accStore,
		ledger:     billing.NewPostgresLedger(pool),
		channels:   channels,
		pool:       pool,
	}
}

func channelKeyCipherForConfig(cfg *config.Config) (*admin.ChannelKeyCipher, error) {
	if cfg == nil || !cfg.Database.Enabled {
		return nil, nil
	}
	enc := cfg.Database.ChannelKeyEncryption
	return admin.NewChannelKeyCipher(enc.KeyID, enc.Key)
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
