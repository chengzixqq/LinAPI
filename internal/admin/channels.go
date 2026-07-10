package admin

import "linapi/internal/routing"

// ChannelToRouting 把管理面渠道视图转换为路由引擎渠道。
// 管理写操作后，用 AdminStore.ListChannels 全量拉取再逐个转换，喂给 router.UpdateChannels。
func ChannelToRouting(c Channel) *routing.Channel {
	models := c.Models
	if models == nil {
		models = map[string]string{}
	}
	return &routing.Channel{
		ID:       c.ChannelID,
		Name:     c.Name,
		Format:   routing.Format(c.Format),
		BaseURL:  c.BaseURL,
		APIKey:   c.APIKey,
		Models:   models,
		Priority: c.Priority,
		Weight:   c.Weight,
		Enabled:  c.Enabled,
	}
}

// ChannelsToRouting 批量转换。
func ChannelsToRouting(cs []Channel) []*routing.Channel {
	out := make([]*routing.Channel, 0, len(cs))
	for _, c := range cs {
		out = append(out, ChannelToRouting(c))
	}
	return out
}
