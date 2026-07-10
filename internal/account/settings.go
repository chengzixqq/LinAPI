package account

import (
	"context"
	"strconv"
)

// 系统设置的 KV 键。
const (
	KeyRegistrationEnabled   = "registration_enabled"
	KeyNewUserInitialBalance = "new_user_initial_balance"
)

// 默认值：注册默认关闭（安全默认），初始额度默认 0。
const (
	DefaultRegistrationEnabled   = false
	DefaultNewUserInitialBalance = int64(0)
)

// Settings 是系统设置的领域视图。
type Settings struct {
	RegistrationEnabled   bool  `json:"registration_enabled"`
	NewUserInitialBalance int64 `json:"new_user_initial_balance"`
}

// SettingsStore 是系统设置数据访问接口。实现须并发安全。
type SettingsStore interface {
	// Get 读取全部设置（缺失的键回退默认值）。
	Get(ctx context.Context) (Settings, error)
	// Put 覆盖写入全部设置。
	Put(ctx context.Context, s Settings) error
}

// parseBool / formatBool / parseInt64 是 KV 值（TEXT）与类型化字段的转换辅助。
func parseBool(s string, def bool) bool {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return v
}

func formatBool(b bool) string { return strconv.FormatBool(b) }

func parseInt64(s string, def int64) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

func formatInt64(n int64) string { return strconv.FormatInt(n, 10) }
