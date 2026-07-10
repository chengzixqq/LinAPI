package admin

import (
	"context"
	"log/slog"

	"linapi/internal/routing"
)

// Service 是管理面的应用服务：封装 AdminStore 之上的编排，
// 关键职责是让渠道写操作（增删改、启停）在落库后即时热更新路由引擎。
//
// 与 router 通过 *routing.Router 直连（同进程）。多实例部署时，其它实例
// 依赖定时热重载（见 server 装配的 reload goroutine）收敛渠道变更。
type Service struct {
	store  AdminStore
	router *routing.Router // 可为 nil（无路由的场景，如纯用户/密钥管理测试）
	logger *slog.Logger
}

// NewService 构建管理服务。router 为 nil 时渠道写操作仍落库，只是不触发热更新。
func NewService(store AdminStore, router *routing.Router, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, router: router, logger: logger}
}

// Store 暴露底层 AdminStore，供只读 handler 直接使用。
func (s *Service) Store() AdminStore { return s.store }

// ReloadChannels 从存储全量拉取渠道并原子替换到路由引擎。
// 渠道写操作后调用；也供定时热重载复用。router 为 nil 时为空操作。
func (s *Service) ReloadChannels(ctx context.Context) error {
	if s.router == nil {
		return nil
	}
	channels, err := s.store.ListChannels(ctx)
	if err != nil {
		return err
	}
	s.router.UpdateChannels(ChannelsToRouting(channels))
	s.logger.Info("路由渠道已热更新", "count", len(channels))
	return nil
}

// ---- 渠道写操作：落库后热更新 ----

// CreateChannel 新建渠道并热更新路由。
func (s *Service) CreateChannel(ctx context.Context, in ChannelInput) (Channel, error) {
	ch, err := s.store.CreateChannel(ctx, in)
	if err != nil {
		return Channel{}, err
	}
	s.reloadAfterWrite(ctx)
	return ch, nil
}

// UpdateChannel 全量更新渠道并热更新路由。
func (s *Service) UpdateChannel(ctx context.Context, in ChannelInput) (Channel, error) {
	ch, err := s.store.UpdateChannel(ctx, in)
	if err != nil {
		return Channel{}, err
	}
	s.reloadAfterWrite(ctx)
	return ch, nil
}

// SetChannelEnabled 启停渠道并热更新路由。
func (s *Service) SetChannelEnabled(ctx context.Context, channelID string, enabled bool) (Channel, error) {
	ch, err := s.store.SetChannelEnabled(ctx, channelID, enabled)
	if err != nil {
		return Channel{}, err
	}
	s.reloadAfterWrite(ctx)
	return ch, nil
}

// DeleteChannel 删除渠道并热更新路由。
func (s *Service) DeleteChannel(ctx context.Context, channelID string) error {
	if err := s.store.DeleteChannel(ctx, channelID); err != nil {
		return err
	}
	s.reloadAfterWrite(ctx)
	return nil
}

// reloadAfterWrite 在写操作成功后热更新路由；失败仅记日志，不影响写操作本身
// （下一次定时重载会最终收敛）。
func (s *Service) reloadAfterWrite(ctx context.Context) {
	if err := s.ReloadChannels(ctx); err != nil {
		s.logger.Error("渠道写操作后热更新路由失败", "err", err)
	}
}
