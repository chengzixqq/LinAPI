package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config 是网关的全部运行时配置。
// 通过配置文件（config.yaml）加载，并允许用环境变量覆盖，
// 环境变量前缀为 LINAPI_，例如 LINAPI_SERVER_PORT=9000。
type Config struct {
	Server   ServerConfig    `mapstructure:"server"`
	Database DatabaseConfig  `mapstructure:"database"`
	Redis    RedisConfig     `mapstructure:"redis"`
	Log      LogConfig       `mapstructure:"log"`
	Auth     AuthConfig      `mapstructure:"auth"`
	Admin    AdminConfig     `mapstructure:"admin"`
	Billing  BillingConfig   `mapstructure:"billing"`
	Upstream UpstreamConfig  `mapstructure:"upstream"`
	Channels []ChannelConfig `mapstructure:"channels"`
}

// ChannelConfig 描述一个上游渠道（过渡期配置来源）。
// database.enabled=true 时渠道改由 channels 表管理，本段仅在内存模式下驱动路由。
type ChannelConfig struct {
	ID       string            `mapstructure:"id"`       // 渠道唯一标识（熔断/日志/计费归因）
	Name     string            `mapstructure:"name"`     // 人类可读名称
	Format   string            `mapstructure:"format"`   // openai | anthropic，决定出向适配器
	BaseURL  string            `mapstructure:"base_url"` // 上游 API 基地址
	APIKey   string            `mapstructure:"api_key"`  // 访问上游的密钥
	Models   map[string]string `mapstructure:"models"`   // 对外模型名 -> 上游实际模型名（值空=透传）
	Priority int               `mapstructure:"priority"` // 优先级，越大越优先
	Weight   int               `mapstructure:"weight"`   // 同优先级内加权随机权重（>0）
	Enabled  bool              `mapstructure:"enabled"`  // false 时不参与选择
}

// UpstreamConfig 定义生产上游目标的网络边界。默认只允许公共 HTTPS；访问内网或
// 明文 HTTP 必须按精确 authority 配规则，不能用全局 broad 开关。
type UpstreamConfig struct {
	TargetRules []UpstreamTargetRuleConfig `mapstructure:"target_rules"`
}

type UpstreamTargetRuleConfig struct {
	Authority    string   `mapstructure:"authority"`     // 规范 host:port
	AllowHTTP    bool     `mapstructure:"allow_http"`    // 仅此 authority 可用 http
	AllowedCIDRs []string `mapstructure:"allowed_cidrs"` // 仅此 authority 可拨的私网 CIDR
}

type ServerConfig struct {
	Port                       int    `mapstructure:"port"`
	Mode                       string `mapstructure:"mode"` // debug | release
	ReadTimeoutSeconds         int    `mapstructure:"read_timeout_seconds"`
	IdleTimeoutSeconds         int    `mapstructure:"idle_timeout_seconds"`
	MaxRequestBodyBytes        int64  `mapstructure:"max_request_body_bytes"`
	MaxHeaderBytes             int    `mapstructure:"max_header_bytes"`
	MetricsToken               string `mapstructure:"metrics_token"`
	MetricsMaxRequestsInFlight int    `mapstructure:"metrics_max_requests_in_flight"`
	MetricsTimeoutSeconds      int    `mapstructure:"metrics_timeout_seconds"`
}

type DatabaseConfig struct {
	// Enabled 为 true 时启用 PostgreSQL（身份/额度/用量日志落库）；
	// 为 false 时回退到配置驱动的内存实现，便于本地开发免装 DB。
	Enabled         bool   `mapstructure:"enabled"`
	DSN             string `mapstructure:"dsn"`
	MaxOpenConns    int    `mapstructure:"max_open_conns"`
	MinIdleConns    int    `mapstructure:"min_idle_conns"`
	ConnMaxLifetime int    `mapstructure:"conn_max_lifetime"` // 秒
	// AutoMigrate 为 true 时启动期幂等应用内置 schema（自动建表）。
	AutoMigrate          bool                       `mapstructure:"auto_migrate"`
	ChannelKeyEncryption ChannelKeyEncryptionConfig `mapstructure:"channel_key_encryption"`
}

// ChannelKeyEncryptionConfig 配置 PostgreSQL 渠道凭证的应用层信封加密。
// Key 应通过环境变量/密钥管理系统注入，不应写入版本库。
type ChannelKeyEncryptionConfig struct {
	KeyID            string `mapstructure:"key_id"`
	Key              string `mapstructure:"key"`
	MigratePlaintext bool   `mapstructure:"migrate_plaintext"`
}

type RedisConfig struct {
	Addr                string         `mapstructure:"addr"`
	Username            string         `mapstructure:"username"`
	Password            string         `mapstructure:"password"`
	DB                  int            `mapstructure:"db"`
	TLS                 RedisTLSConfig `mapstructure:"tls"`
	AllowInsecureRemote bool           `mapstructure:"allow_insecure_remote"`
}

type RedisTLSConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	ServerName string `mapstructure:"server_name"`
	CAFile     string `mapstructure:"ca_file"`
	CertFile   string `mapstructure:"cert_file"`
	KeyFile    string `mapstructure:"key_file"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`  // debug | info | warn | error
	Format string `mapstructure:"format"` // json | text
}

// AuthConfig 是鉴权与额度的过渡期配置来源。
// 第 7 步接入 sqlc/PostgreSQL 后，密钥与额度改由数据库管理，本段将退役。
type AuthConfig struct {
	// Keys 是预置的 API Key 列表，驱动内存 Store。
	Keys []KeyConfig `mapstructure:"keys"`
	// UnauthenticatedRateLimitPerMin 是 /v1 鉴权查库前的每来源 IP 请求预算。
	UnauthenticatedRateLimitPerMin int `mapstructure:"unauthenticated_rate_limit_per_min"`
	// AccountRateLimitPerMin 是所有 Key 共享的账户级总请求预算。
	AccountRateLimitPerMin int `mapstructure:"account_rate_limit_per_min"`
}

// KeyConfig 描述一个预置 API Key。
type KeyConfig struct {
	APIKey          string   `mapstructure:"api_key"`
	KeyID           string   `mapstructure:"key_id"`
	UserID          string   `mapstructure:"user_id"`
	RateLimitPerMin int      `mapstructure:"rate_limit_per_min"`
	AllowedModels   []string `mapstructure:"allowed_models"`
	Enabled         bool     `mapstructure:"enabled"`
	InitialBalance  int64    `mapstructure:"initial_balance"` // 最小计费单位
}

// AdminConfig 是管理面与控制台的配置。
// 控制台鉴权改为「账号密码 + 会话」，不再用裸 token；本段仅保留挂载开关、
// 首个管理员播种（bootstrap）与渠道定时热重载间隔。
type AdminConfig struct {
	// Enabled 为 true 时挂载控制台（/console）与认证端点（/auth /admin /me）；默认关闭。
	Enabled bool `mapstructure:"enabled"`
	// Bootstrap 是首个管理员账户的播种配置（仅当该用户名不存在时创建）。
	Bootstrap BootstrapConfig `mapstructure:"bootstrap"`
	// ChannelReloadInterval 是渠道定时热重载的间隔（秒）。<=0 关闭。仅 database.enabled=true 生效。
	ChannelReloadInterval int `mapstructure:"channel_reload_interval"`
	// AuthRateLimitPerMin 是匿名认证端点（/auth/login、/auth/register）每来源 IP 每分钟的
	// 请求上限，在 bcrypt 之前拦截，堵住在线撞库与 CPU 耗尽（审查 AUD-P1-27）。<=0 关闭限流。
	AuthRateLimitPerMin int `mapstructure:"auth_rate_limit_per_min"`
	// AuthIdentifierRateLimitPerMin 是每个归一化登录名、每个认证端点的分钟预算。
	// 它与 IP 预算叠加，限制分布式撞库；Redis key 只保存登录名摘要。
	AuthIdentifierRateLimitPerMin int `mapstructure:"auth_identifier_rate_limit_per_min"`
	// MaxActiveSessionsPerAccount 限制单账户同时有效的控制台会话数。
	MaxActiveSessionsPerAccount int `mapstructure:"max_active_sessions_per_account"`
}

// BootstrapConfig 描述首个管理员账户的播种参数。
type BootstrapConfig struct {
	// Username 为空时不播种。
	Username string `mapstructure:"username"`
	// Password 建议用环境变量注入（LINAPI_ADMIN_BOOTSTRAP_PASSWORD）。为空时不播种并告警。
	Password string `mapstructure:"password"`
}

// BillingConfig 是计费模块配置。单价单位：最小计费单位 / 每 100 万 token。
type BillingConfig struct {
	// DefaultReserve 是预授权下限；模型最大成本低于它时仍至少冻结该金额。
	DefaultReserve int64 `mapstructure:"default_reserve"`
	// DefaultInputPer1M / DefaultOutputPer1M 是未在 Models 中命中的兜底单价。
	DefaultInputPer1M              int64 `mapstructure:"default_input_per_1m"`
	DefaultOutputPer1M             int64 `mapstructure:"default_output_per_1m"`
	DefaultCacheCreationInputPer1M int64 `mapstructure:"default_cache_creation_input_per_1m"`
	DefaultCacheReadInputPer1M     int64 `mapstructure:"default_cache_read_input_per_1m"`
	// 默认模型计费边界。未知模型也必须有非零上界，才能在发上游前冻结最坏成本。
	DefaultMaxBillableInputTokens int `mapstructure:"default_max_billable_input_tokens"`
	DefaultMaxOutputTokens        int `mapstructure:"default_max_output_tokens"`
	// OpenAIOutputLimitFields 按 "channel_id/upstream_model" 或 "upstream_model"
	// 指定上游实际识别的输出上限字段：max_tokens 或 max_completion_tokens。
	// release 模式要求所有 OpenAI 渠道模型均命中显式策略，不能信任客户端字段。
	OpenAIOutputLimitFields map[string]string `mapstructure:"openai_output_limit_fields"`
	// Models 是按对外模型名配置的单价表。
	Models []ModelPriceConfig `mapstructure:"models"`
}

// ModelPriceConfig 描述单个模型的计价。
type ModelPriceConfig struct {
	Model                   string `mapstructure:"model"`
	InputPer1M              int64  `mapstructure:"input_per_1m"`
	OutputPer1M             int64  `mapstructure:"output_per_1m"`
	CacheCreationInputPer1M int64  `mapstructure:"cache_creation_input_per_1m"`
	CacheReadInputPer1M     int64  `mapstructure:"cache_read_input_per_1m"`
	MaxBillableInputTokens  int    `mapstructure:"max_billable_input_tokens"`
	MaxOutputTokens         int    `mapstructure:"max_output_tokens"`
}

// Load 读取配置：优先级为 环境变量 > 配置文件 > 默认值。
func Load(path string) (*Config, error) {
	v := viper.New()

	setDefaults(v)

	// 配置文件（可选：不存在时仅用默认值 + 环境变量）
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) && !os.IsNotExist(err) {
				return nil, fmt.Errorf("读取配置文件失败: %w", err)
			}
		}
	}

	// 环境变量覆盖：LINAPI_SERVER_PORT -> server.port
	v.SetEnvPrefix("LINAPI")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.mode", "release")
	v.SetDefault("server.read_timeout_seconds", 30)
	v.SetDefault("server.idle_timeout_seconds", 120)
	v.SetDefault("server.max_request_body_bytes", 32*1024*1024)
	v.SetDefault("server.max_header_bytes", 64*1024)
	v.SetDefault("server.metrics_token", "")
	v.SetDefault("server.metrics_max_requests_in_flight", 2)
	v.SetDefault("server.metrics_timeout_seconds", 10)

	v.SetDefault("database.enabled", false)
	v.SetDefault("database.dsn", "postgres://postgres:postgres@localhost:5432/linapi?sslmode=disable")
	v.SetDefault("database.max_open_conns", 50)
	v.SetDefault("database.min_idle_conns", 10)
	v.SetDefault("database.conn_max_lifetime", 3600)
	v.SetDefault("database.auto_migrate", true)
	v.SetDefault("database.channel_key_encryption.key_id", "")
	v.SetDefault("database.channel_key_encryption.key", "")
	v.SetDefault("database.channel_key_encryption.migrate_plaintext", false)

	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("redis.username", "")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)
	v.SetDefault("redis.tls.enabled", false)
	v.SetDefault("redis.tls.server_name", "")
	v.SetDefault("redis.tls.ca_file", "")
	v.SetDefault("redis.tls.cert_file", "")
	v.SetDefault("redis.tls.key_file", "")
	v.SetDefault("redis.allow_insecure_remote", false)

	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("auth.unauthenticated_rate_limit_per_min", 120)
	v.SetDefault("auth.account_rate_limit_per_min", 10000)

	// 管理面/控制台默认关闭，需显式开启。bootstrap 默认空（不播种）。
	// 渠道定时热重载默认 60s（database.enabled=true 时生效，<=0 关闭）。
	v.SetDefault("admin.enabled", false)
	v.SetDefault("admin.bootstrap.username", "")
	v.SetDefault("admin.bootstrap.password", "")
	v.SetDefault("admin.channel_reload_interval", 60)
	// 匿名认证端点每 IP 每分钟上限，bcrypt 前拦截撞库（审查 AUD-P1-27）。默认 30 次/分钟：
	// 正常用户登录绰绰有余，暴力破解则被卡死。<=0 关闭。
	v.SetDefault("admin.auth_rate_limit_per_min", 30)
	v.SetDefault("admin.auth_identifier_rate_limit_per_min", 20)
	v.SetDefault("admin.max_active_sessions_per_account", 10)

	// 计费默认值：默认预扣额与兜底单价。单价 = 最小计费单位 / 每 100 万 token。
	// 这里给出保守的非零兜底，避免误配为 0 导致「免费」漏洞。
	v.SetDefault("billing.default_reserve", 10000)
	v.SetDefault("billing.default_input_per_1m", 2_000_000)
	v.SetDefault("billing.default_output_per_1m", 6_000_000)
	v.SetDefault("billing.default_cache_creation_input_per_1m", 2_500_000)
	v.SetDefault("billing.default_cache_read_input_per_1m", 200_000)
	v.SetDefault("billing.default_max_billable_input_tokens", 128_000)
	v.SetDefault("billing.default_max_output_tokens", 4_096)
}
