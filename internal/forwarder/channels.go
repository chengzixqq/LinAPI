// Package forwarder 是网关的转发层：把「适配器 + 路由 + 熔断 + 计费」串起来，
// 真正向上游供应商发起 HTTP 请求，并把响应反向转换回客户端格式。
//
// 请求生命周期（中间件之后）：
//
//	按客户端格式 ParseRequest 解析为 canonical
//	 → 路由 Select 拿候选渠道（已按优先级/权重排序、过滤熔断）
//	 → 逐候选：Breaker.Allow() 准入 → 按渠道格式 BuildRequest 发上游
//	          → 通过许可回报成功/失败；客户端取消以中性结果停止
//	 → 计费结算：Settle（按真实用量退差 + 记用量日志）或 Refund（全败全额退押金）
//
// 与 routing 包的约定：拿到 []Candidate 后必须对每个尝试的候选调用 Breaker.Allow()，
// 并通过返回的许可配对回报结果，否则半开探测额度会泄漏、熔断器卡死。
package forwarder

import (
	"encoding/json"
	"fmt"

	"linapi/internal/config"
	"linapi/internal/db"
	"linapi/internal/routing"
)

// ChannelsFromConfig 把配置中的渠道列表转换为路由引擎的 Channel。
// 用于内存模式（database.enabled=false）下的渠道来源。
func ChannelsFromConfig(cfgs []config.ChannelConfig) []*routing.Channel {
	channels := make([]*routing.Channel, 0, len(cfgs))
	for _, c := range cfgs {
		channels = append(channels, &routing.Channel{
			ID:       c.ID,
			Name:     c.Name,
			Format:   routing.Format(c.Format),
			BaseURL:  c.BaseURL,
			APIKey:   c.APIKey,
			Models:   c.Models,
			Priority: c.Priority,
			Weight:   c.Weight,
			Enabled:  c.Enabled,
		})
	}
	return channels
}

// ChannelsFromDB 把数据库中的启用渠道行转换为路由引擎的 Channel。
// Models 列在库中是 JSONB，这里解组为「对外模型名 -> 上游实际模型名」映射；
// 解组失败的渠道会被跳过并返回错误，避免坏数据静默污染路由。
func ChannelsFromDB(rows []db.ListEnabledChannelsRow) ([]*routing.Channel, error) {
	channels := make([]*routing.Channel, 0, len(rows))
	for _, r := range rows {
		models := map[string]string{}
		if len(r.Models) > 0 {
			if err := json.Unmarshal(r.Models, &models); err != nil {
				return nil, fmt.Errorf("forwarder: 解析渠道 %q 的 models 失败: %w", r.ChannelID, err)
			}
		}
		channels = append(channels, &routing.Channel{
			ID:       r.ChannelID,
			Name:     r.Name,
			Format:   routing.Format(r.Format),
			BaseURL:  r.BaseURL,
			APIKey:   r.ApiKey,
			Models:   models,
			Priority: int(r.Priority),
			Weight:   int(r.Weight),
			Enabled:  r.Enabled,
		})
	}
	return channels, nil
}
