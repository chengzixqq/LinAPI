package forwarder

import "sort"

// Models 聚合所有启用渠道对外暴露的模型名，去重后按字典序返回。
// 供 /v1/models 端点列出网关可服务的模型清单。
//
// 只统计启用渠道（Enabled=true）的模型；被熔断的渠道仍算“可服务”，
// 因为熔断是瞬时健康状态，不代表模型不再提供。
func (f *Forwarder) Models() []string {
	channels := f.router.Channels()
	seen := make(map[string]struct{})
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		for model := range ch.Models {
			seen[model] = struct{}{}
		}
	}
	models := make([]string, 0, len(seen))
	for m := range seen {
		models = append(models, m)
	}
	sort.Strings(models)
	return models
}
