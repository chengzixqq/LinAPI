package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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

	// v1 兼容端点分组：鉴权 -> 限流 -> 额度闸门
	v1 := s.engine.Group("/v1")
	v1.Use(
		middleware.Auth(s.deps.Store),
		rateLimiter.Middleware(),
		middleware.Quota(s.deps.Store, s.deps.Billing),
	)
	{
		v1.POST("/chat/completions", s.deps.Forwarder.Handler("openai")) // OpenAI 兼容
		v1.POST("/messages", s.deps.Forwarder.Handler("anthropic"))      // Claude 兼容
		v1.GET("/models", s.listModels)
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
	h := newAuthHandlers(s.deps.Account, s.deps.Settings, s.deps.Session, s.deps.SecureCookie)
	g := s.engine.Group("/auth")
	g.POST("/register", h.register)
	g.POST("/login", h.login)
	g.POST("/logout", middleware.SessionAuth(s.deps.Session), h.logout)
	g.GET("/me", middleware.SessionAuth(s.deps.Session), h.me)
}

// registerMeRoutes 挂载 /me 分组（用户自助）。需登录（任意角色）。
func (s *Server) registerMeRoutes() {
	if !s.cfg.Admin.Enabled || s.deps.Account == nil || s.deps.Session == nil || s.deps.Admin == nil {
		return
	}
	h := newMeHandlers(s.deps.Admin, s.deps.Store)
	g := s.engine.Group("/me", middleware.SessionAuth(s.deps.Session))
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
	// 管理面改「会话 + admin 角色」鉴权（替换裸 token）。
	g := s.engine.Group("/admin", middleware.SessionAuth(s.deps.Session), middleware.RequireRole(account.RoleAdmin))
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
