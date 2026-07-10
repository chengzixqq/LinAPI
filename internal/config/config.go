package config

import (
	"fmt"
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

type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"` // debug | release
}

type DatabaseConfig struct {
	// Enabled 为 true 时启用 PostgreSQL（身份/额度/用量日志落库）；
	// 为 false 时回退到配置驱动的内存实现，便于本地开发免装 DB。
	Enabled         bool   `mapstructure:"enabled"`
	DSN             string `mapstructure:"dsn"`
	MaxOpenConns    int    `mapstructure:"max_open_conns"`
	MaxIdleConns    int    `mapstructure:"max_idle_conns"`
	ConnMaxLifetime int    `mapstructure:"conn_max_lifetime"` // 秒
	// AutoMigrate 为 true 时启动期幂等应用内置 schema（自动建表）。
	AutoMigrate bool `mapstructure:"auto_migrate"`
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
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

// AdminConfig 是管理面（用户/密钥/渠道 CRUD）的配置。
// 管理端点与业务 /v1 端点鉴权隔离：需独立的 admin token，且可选仅允许回环地址访问。
type AdminConfig struct {
	// Enabled 为 true 时挂载 /admin/* 管理端点；默认关闭（最小暴露面）。
	Enabled bool `mapstructure:"enabled"`
	// Token 是管理端点的鉴权令牌（Authorization: Bearer <token>）。
	// Enabled=true 但 Token 为空时启动报错——绝不允许无鉴权的管理面。
	Token string `mapstructure:"token"`
	// LoopbackOnly 为 true 时只接受来自回环地址（127.0.0.1/::1）的管理请求，
	// 与 token 叠加形成双重防线。
	LoopbackOnly bool `mapstructure:"loopback_only"`
	// ChannelReloadInterval 是渠道定时热重载的间隔（秒）。
	// <=0 表示关闭定时重载（仅管理写操作即时热更新）。仅 database.enabled=true 时生效。
	ChannelReloadInterval int `mapstructure:"channel_reload_interval"`
}

// BillingConfig 是计费模块配置。单价单位：最小计费单位 / 每 100 万 token。
type BillingConfig struct {
	// DefaultReserve 是无法预估时每次请求的默认预扣（押金）额，Settle 时按真实用量退差。
	DefaultReserve int64 `mapstructure:"default_reserve"`
	// DefaultInputPer1M / DefaultOutputPer1M 是未在 Models 中命中的兜底单价。
	DefaultInputPer1M  int64 `mapstructure:"default_input_per_1m"`
	DefaultOutputPer1M int64 `mapstructure:"default_output_per_1m"`
	// Models 是按对外模型名配置的单价表。
	Models []ModelPriceConfig `mapstructure:"models"`
}

// ModelPriceConfig 描述单个模型的计价。
type ModelPriceConfig struct {
	Model       string `mapstructure:"model"`
	InputPer1M  int64  `mapstructure:"input_per_1m"`
	OutputPer1M int64  `mapstructure:"output_per_1m"`
}

// Load 读取配置：优先级为 环境变量 > 配置文件 > 默认值。
func Load(path string) (*Config, error) {
	v := viper.New()

	setDefaults(v)

	// 配置文件（可选：不存在时仅用默认值 + 环境变量）
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
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

	v.SetDefault("database.enabled", false)
	v.SetDefault("database.dsn", "postgres://postgres:postgres@localhost:5432/linapi?sslmode=disable")
	v.SetDefault("database.max_open_conns", 50)
	v.SetDefault("database.max_idle_conns", 10)
	v.SetDefault("database.conn_max_lifetime", 3600)
	v.SetDefault("database.auto_migrate", true)

	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)

	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

	// 管理面默认关闭，需显式开启并配置 token。回环限制默认关闭；
	// 渠道定时热重载默认 60s（database.enabled=true 时生效，<=0 关闭）。
	v.SetDefault("admin.enabled", false)
	v.SetDefault("admin.token", "")
	v.SetDefault("admin.loopback_only", false)
	v.SetDefault("admin.channel_reload_interval", 60)

	// 计费默认值：默认预扣额与兜底单价。单价 = 最小计费单位 / 每 100 万 token。
	// 这里给出保守的非零兜底，避免误配为 0 导致「免费」漏洞。
	v.SetDefault("billing.default_reserve", 10000)
	v.SetDefault("billing.default_input_per_1m", 2_000_000)
	v.SetDefault("billing.default_output_per_1m", 6_000_000)
}
