package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"linapi/internal/account"
	"linapi/internal/admin"
	"linapi/internal/billing"
	"linapi/internal/config"
	"linapi/internal/forwarder"
	"linapi/internal/middleware"
	"linapi/internal/session"
	"linapi/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// Server 封装 HTTP 服务，负责生命周期管理与优雅关闭。
type Server struct {
	cfg    *config.Config
	deps   Deps
	engine *gin.Engine
	http   *http.Server
}

// Deps 是 Server 的外部依赖，由 main 构建后注入，便于测试替换。
type Deps struct {
	Store        store.Store           // 身份/额度数据访问
	Redis        *redis.Client         // 限流等分布式状态
	Billing      *billing.Billing      // 计费门面（预扣/结算）
	Forwarder    *forwarder.Forwarder  // 转发层（适配器 + 路由 + 熔断 + 结算）
	Admin        *admin.Service        // 管理面服务（用户/密钥/渠道 CRUD）；nil 表示不挂管理端点
	Account      account.AccountStore  // 控制台账户数据访问；nil 表示不挂控制台端点
	Settings     account.SettingsStore // 系统设置数据访问
	Session      *session.Manager      // 会话管理器（Redis）
	SecureCookie bool                  // 会话 Cookie 是否加 Secure 属性（生产 HTTPS 置 true）
	Logger       *slog.Logger          // 结构化日志器；nil 时 RequestLogger 退化为 slog.Default()
}

// New 构建一个 Server，注册中间件与路由，但不启动监听。
func New(cfg *config.Config, deps Deps) *Server {
	gin.SetMode(cfg.Server.Mode)

	engine := gin.New()
	engine.Use(gin.Recovery())

	s := &Server{
		cfg:    cfg,
		deps:   deps,
		engine: engine,
	}
	s.registerRoutes()

	s.http = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		// 注意：不设置 WriteTimeout —— 流式（SSE）响应可能持续数分钟，
		// 写超时会在长回复中途掐断连接。
	}

	return s
}

// registerRoutes 挂载路由。/v1 分组按 鉴权 -> 限流 -> 额度 顺序过中间件，
// 之后接入 OpenAI/Claude/Gemini 兼容端点。
func (s *Server) registerRoutes() {
	// 结构化访问日志：入口分配 request_id（注入 context + 响应头），收尾输出
	// 方法/路径/状态/耗时/身份/模型/渠道/用量。跳过探活与指标端点避免噪声。
	// 置于最前（Recovery 之后），使 request_id 对全链路可见、耗时覆盖完整处理。
	s.engine.Use(middleware.RequestLogger(s.deps.Logger, "/healthz", "/metrics"))

	// 全局指标中间件：记录所有对外请求的计数与耗时（含 /healthz、/v1、/admin）。
	s.engine.Use(middleware.Metrics())

	// 健康检查：探活与就绪探测用，不走鉴权。
	s.engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Prometheus 指标暴露端点。不走鉴权，依赖部署层网络隔离（仅内网/监控可达）。
	s.engine.GET("/metrics", gin.WrapH(promhttp.Handler()))

	rateLimiter := middleware.NewRateLimiter(s.deps.Redis)

	// v1 兼容端点分组。
	//
	// 中间件分层：Auth + RateLimit 是所有 /v1 端点的公共前置；Quota（预扣押金）
	// 只加在真正产生上游用量的生成端点上。/models 是只读元数据端点，不发上游、
	// 无用量，若也过 Quota 会每查一次就永久扣掉一笔 default_reserve 不退
	// （审查 AUD-P1-01）——故它只经 Auth+RateLimit，绝不预扣。
	v1 := s.engine.Group("/v1")
	v1.Use(
		middleware.Auth(s.deps.Store),
		rateLimiter.Middleware(),
	)
	{
		v1.GET("/models", s.listModels)

		// 生成端点子组：额外叠加 Quota 预扣闸门。
		gen := v1.Group("")
		gen.Use(middleware.Quota(s.deps.Store, s.deps.Billing))
		gen.POST("/chat/completions", s.deps.Forwarder.Handler("openai")) // OpenAI 兼容
		gen.POST("/messages", s.deps.Forwarder.Handler("anthropic"))      // Claude 兼容
	}

	s.registerAuthRoutes()
	s.registerMeRoutes()
	s.registerAdminRoutes()
}

// registerAuthRoutes 挂载 /auth 分组（注册/登录/登出/me）。
// 仅当 admin.enabled=true 且注入了账户体系依赖时生效。
func (s *Server) registerAuthRoutes() {
	if !s.cfg.Admin.Enabled || s.deps.Account == nil || s.deps.Settings == nil || s.deps.Session == nil {
		return
	}
	// bcrypt 并发信号量（审查 AUD-P1-27）：容量取 CPU 核数——bcrypt 是 CPU 密集，
	// 并发度约等于核数最优，多余请求排队而非无界堆积 goroutine 打满 CPU。
	bcryptSem := middleware.NewSemaphore(runtime.NumCPU())
	h := newAuthHandlers(s.deps.Account, s.deps.Settings, s.deps.Session, s.deps.SecureCookie, bcryptSem)

	g := s.engine.Group("/auth")
	// 登录/注册是匿名端点，在 bcrypt 之前按来源 IP 限流，堵住撞库与 CPU 耗尽。
	// logout/me 已由 SessionAuth 天然限制（需有效会话），无需再叠 IP 限流。
	authLimiter := middleware.NewIPRateLimiter(s.deps.Redis, "auth", s.cfg.Admin.AuthRateLimitPerMin)
	// 会话代次校验（审查 AUD-P1-17）：logout/me 用带代次的鉴权，账户禁用/改密后旧会话立即失效。
	sessAuth := middleware.SessionAuthWithVersion(s.deps.Session, s.sessionVersionChecker())
	g.POST("/register", authLimiter.Middleware(), h.register)
	g.POST("/login", authLimiter.Middleware(), h.login)
	g.POST("/logout", sessAuth, h.logout)
	g.GET("/me", sessAuth, h.me)
}

// registerMeRoutes 挂载 /me 分组（用户自助）。需登录（任意角色）。
func (s *Server) registerMeRoutes() {
	// 守卫对齐 handler 实际依赖：newMeHandlers 用 Admin+Store，分组挂 SessionAuth 用 Session。
	// 缺任一则整组不挂（fail-closed），绝不挂出裸奔或请求期 nil-panic 的端点。
	if !s.cfg.Admin.Enabled || s.deps.Admin == nil || s.deps.Store == nil || s.deps.Session == nil {
		return
	}
	h := newMeHandlers(s.deps.Admin, s.deps.Store)
	// SessionAuth 注入会话后叠 CSRFProtect：/me 的写操作（建/删/启停 key）经 Cookie 鉴权，
	// 须过 CSRF 校验（审查 AUD-P1-26）；GET 由中间件自动放行。
	// 鉴权用带会话代次校验的形式（审查 AUD-P1-17）：账户禁用/改密后旧会话立即失效。
	g := s.engine.Group("/me", middleware.SessionAuthWithVersion(s.deps.Session, s.sessionVersionChecker()), middleware.CSRFProtect())
	g.GET("/profile", h.profile)
	g.GET("/keys", h.listKeys)
	g.POST("/keys", h.createKey)
	g.PATCH("/keys/:keyid/enabled", h.setKeyEnabled)
	g.DELETE("/keys/:keyid", h.deleteKey)
}

// registerAdminRoutes 挂载管理面 /admin 分组。
//
// 鉴权改为「会话 + admin 角色」（替换 Task 9 移除的裸 token AdminAuth）：
// 先 SessionAuth 校验登录会话，再 RequireRole 校验角色为 admin，缺一不可。
func (s *Server) registerAdminRoutes() {
	if !s.cfg.Admin.Enabled || s.deps.Admin == nil || s.deps.Account == nil || s.deps.Settings == nil || s.deps.Session == nil {
		return
	}

	h := &adminHandlers{svc: s.deps.Admin}
	ac := newAccountConsoleHandlers(s.deps.Account, s.deps.Settings)
	// 管理面改「会话 + admin 角色」鉴权（替换裸 token），再叠 CSRFProtect 守护写操作
	// （审查 AUD-P1-26）：账户/设置/渠道等一切写端点均经 Cookie 鉴权，须过 CSRF 校验。
	// 会话鉴权用带代次校验的形式（审查 AUD-P1-17）：账户禁用/改密后旧会话（含被禁管理员的）立即失效。
	g := s.engine.Group("/admin", middleware.SessionAuthWithVersion(s.deps.Session, s.sessionVersionChecker()), middleware.RequireRole(account.RoleAdmin), middleware.CSRFProtect())
	{
		// 账户与系统设置
		g.GET("/accounts", ac.listAccounts)
		g.POST("/accounts", ac.createAccount)
		g.PATCH("/accounts/:id/enabled", ac.setAccountEnabled)
		g.POST("/accounts/:id/password", ac.resetPassword)
		g.GET("/settings", ac.getSettings)
		g.PUT("/settings", ac.putSettings)

		// 计费用户
		g.POST("/users", h.createUser)
		g.GET("/users", h.listUsers)
		g.GET("/users/:id", h.getUser)
		g.PATCH("/users/:id/enabled", h.setUserEnabled)
		g.POST("/users/:id/balance", h.addBalance)

		// 密钥（挂在用户下）
		g.POST("/users/:id/keys", h.createKey)
		g.GET("/users/:id/keys", h.listKeys)
		g.PATCH("/keys/:keyid/enabled", h.setKeyEnabled)

		// 渠道
		g.POST("/channels", h.createChannel)
		g.GET("/channels", h.listChannels)
		g.GET("/channels/:id", h.getChannel)
		g.PUT("/channels/:id", h.updateChannel)
		g.PATCH("/channels/:id/enabled", h.setChannelEnabled)
		g.DELETE("/channels/:id", h.deleteChannel)
	}
}

// sessionVersionChecker 把 account.AccountStore 适配为 middleware.SessionVersionChecker
// （审查 AUD-P1-17）：按会话里的 AccountID 回查账户当前代次，供鉴权比对。账户已删除
// （ErrNotFound）时向上返回错误，由中间件 fail-closed 拒绝——账户没了，旧会话不该再有效。
func (s *Server) sessionVersionChecker() middleware.SessionVersionChecker {
	return middleware.SessionVersionCheckerFunc(func(ctx context.Context, accountID int64) (int, error) {
		acc, err := s.deps.Account.GetByID(ctx, accountID)
		if err != nil {
			return 0, err
		}
		return acc.SessionVersion, nil
	})
}

func placeholder(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": gin.H{
			"message": "该端点尚未实现",
			"type":    "not_implemented",
		},
	})
}

// writeError 以统一的错误结构响应（对齐 OpenAI 风格），供各 handler 复用。
func writeError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errType,
		},
	})
}

// listModels 实现 GET /v1/models：返回网关可服务的模型清单（OpenAI 格式）。
// 模型名从路由引擎的启用渠道聚合去重。
func (s *Server) listModels(c *gin.Context) {
	models := s.deps.Forwarder.Models()
	now := time.Now().Unix()
	data := make([]gin.H, 0, len(models))
	for _, m := range models {
		data = append(data, gin.H{
			"id":       m,
			"object":   "model",
			"created":  now,
			"owned_by": "linapi",
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// Start 启动 HTTP 监听（阻塞直到服务停止）。
func (s *Server) Start() error {
	if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown 优雅关闭，等待进行中的请求完成或超时。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// Addr 返回监听地址，便于日志输出。
func (s *Server) Addr() string {
	return s.http.Addr
}
