// Package routing 实现渠道路由与负载均衡引擎。
//
// 职责：把一个“对外模型名”的请求，解析为一组可服务它的上游渠道，
// 按优先级 + 加权策略排序，并结合熔断状态给出可依次尝试的候选序列，
// 支撑故障转移。本包是纯逻辑，不发起网络请求——真实转发由上层执行。
package routing

// Format 标识渠道使用的供应商线格式，决定出向使用哪个适配器。
type Format string

const (
	FormatOpenAI    Format = "openai"
	FormatAnthropic Format = "anthropic"
)

// Channel 表示一个上游渠道（一个供应商端点 + 凭证 + 能力）。
type Channel struct {
	// ID 是渠道唯一标识（稳定，用于熔断状态、日志、计费归因）。
	ID string

	// Name 是人类可读名称。
	Name string

	// Format 决定该渠道用哪种线格式与上游通信。
	Format Format

	// BaseURL 是上游 API 基地址（如 https://api.openai.com）。
	BaseURL string

	// APIKey 是访问上游的密钥。
	APIKey string

	// Models 是“对外模型名 -> 上游实际模型名”的映射。
	// 例如对外 "gpt-4o" 在该渠道映射为 "gpt-4o-2024-08-06"。
	// 值为空字符串表示原样透传。
	Models map[string]string

	// Priority 是优先级，数值越大越优先被选择。
	Priority int

	// Weight 是同优先级内加权随机的权重（>0）。
	Weight int

	// Enabled 为 false 时该渠道完全不参与选择。
	Enabled bool
}

// Supports 返回该渠道是否声明支持某对外模型名。
func (c *Channel) Supports(model string) bool {
	if !c.Enabled {
		return false
	}
	_, ok := c.Models[model]
	return ok
}

// UpstreamModel 返回对外模型名在该渠道对应的上游实际模型名。
// 若映射值为空则原样返回对外名（透传）。
func (c *Channel) UpstreamModel(model string) string {
	if v, ok := c.Models[model]; ok && v != "" {
		return v
	}
	return model
}
