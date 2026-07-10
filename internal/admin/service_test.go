package admin

import (
	"context"
	"errors"
	"testing"

	"linapi/internal/routing"
	"linapi/internal/store"
)

// ---- 渠道 CRUD ----

func TestMemoryChannelCRUD(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()

	in := ChannelInput{
		ChannelID: "c1", Name: "chan-1", Format: "openai",
		BaseURL: "https://up.example", APIKey: "sk-up",
		Models: map[string]string{"gpt-4o": ""}, Priority: 10, Weight: 1, Enabled: true,
	}
	ch, err := m.CreateChannel(ctx, in)
	if err != nil {
		t.Fatalf("CreateChannel 失败: %v", err)
	}
	if ch.ChannelID != "c1" || ch.Format != "openai" || !ch.Enabled {
		t.Fatalf("渠道字段不符: %+v", ch)
	}

	// 重复 channel_id -> ErrConflict。
	if _, err := m.CreateChannel(ctx, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复渠道应 ErrConflict, 得到 %v", err)
	}

	got, err := m.GetChannel(ctx, "c1")
	if err != nil || got.BaseURL != "https://up.example" {
		t.Fatalf("GetChannel 失败: %+v err=%v", got, err)
	}
	if _, err := m.GetChannel(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("查不存在渠道应 ErrNotFound, 得到 %v", err)
	}

	// 全量更新：改优先级与模型映射，createdAt 应保留。
	upd := in
	upd.Priority = 99
	upd.Models = map[string]string{"gpt-4o": "gpt-4o-mini"}
	updated, err := m.UpdateChannel(ctx, upd)
	if err != nil || updated.Priority != 99 {
		t.Fatalf("UpdateChannel 失败: %+v err=%v", updated, err)
	}
	if !updated.CreatedAt.Equal(ch.CreatedAt) {
		t.Errorf("更新应保留 CreatedAt: 原 %v 新 %v", ch.CreatedAt, updated.CreatedAt)
	}
	if _, err := m.UpdateChannel(ctx, ChannelInput{ChannelID: "ghost", Format: "openai"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在渠道应 ErrNotFound, 得到 %v", err)
	}

	// 启停。
	off, err := m.SetChannelEnabled(ctx, "c1", false)
	if err != nil || off.Enabled {
		t.Fatalf("SetChannelEnabled(false) 失败: %+v err=%v", off, err)
	}

	// 删除。
	if err := m.DeleteChannel(ctx, "c1"); err != nil {
		t.Fatalf("DeleteChannel 失败: %v", err)
	}
	if _, err := m.GetChannel(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应查不到, 得到 %v", err)
	}
	if err := m.DeleteChannel(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除不存在渠道应 ErrNotFound, 得到 %v", err)
	}
}

// TestMemoryListChannelsSorted 验证列表按优先级降序、同优先级按 ID 升序。
func TestMemoryListChannelsSorted(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	seed := []ChannelInput{
		{ChannelID: "b", Format: "openai", BaseURL: "u", Priority: 5, Weight: 1, Enabled: true},
		{ChannelID: "a", Format: "openai", BaseURL: "u", Priority: 5, Weight: 1, Enabled: true},
		{ChannelID: "hi", Format: "openai", BaseURL: "u", Priority: 20, Weight: 1, Enabled: true},
	}
	for _, in := range seed {
		if _, err := m.CreateChannel(ctx, in); err != nil {
			t.Fatalf("准备渠道 %s 失败: %v", in.ChannelID, err)
		}
	}
	list, err := m.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels 失败: %v", err)
	}
	want := []string{"hi", "a", "b"} // 优先级 20 在前；同为 5 时 a<b。
	if len(list) != len(want) {
		t.Fatalf("渠道数不符: 期望 %d 得到 %d", len(want), len(list))
	}
	for i, id := range want {
		if list[i].ChannelID != id {
			t.Errorf("位置 %d 期望 %q 得到 %q", i, id, list[i].ChannelID)
		}
	}
}

// ---- Service 热更新（核心风险点）----

// newTestService 建一个空路由 + Service。
func newTestService(t *testing.T) (*Service, *routing.Router) {
	t.Helper()
	r := routing.NewRouter(nil, routing.BreakerConfig{})
	m := NewMemoryStore(store.NewMemoryStore(nil), nil)
	svc := NewService(m, r, nil)
	return svc, r
}

// TestServiceCreateChannelHotReload 验证渠道创建后 router 立即可选中该模型。
func TestServiceCreateChannelHotReload(t *testing.T) {
	svc, r := newTestService(t)
	ctx := context.Background()

	// 创建前：router 选不到。
	if _, err := r.Select("gpt-4o"); !errors.Is(err, routing.ErrNoChannel) {
		t.Fatalf("创建前应无渠道, 得到 %v", err)
	}

	if _, err := svc.CreateChannel(ctx, ChannelInput{
		ChannelID: "c1", Format: "openai", BaseURL: "u",
		Models: map[string]string{"gpt-4o": ""}, Priority: 10, Weight: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateChannel 失败: %v", err)
	}

	// 创建后：router 应立即选中（热更新生效）。
	cands, err := r.Select("gpt-4o")
	if err != nil {
		t.Fatalf("创建后应选到渠道, 得到 %v", err)
	}
	if len(cands) != 1 || cands[0].Channel.ID != "c1" {
		t.Fatalf("热更新后候选不符: %+v", cands)
	}
}

// TestServiceDeleteChannelHotReload 验证删除渠道后 router 立即选不到。
func TestServiceDeleteChannelHotReload(t *testing.T) {
	svc, r := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateChannel(ctx, ChannelInput{
		ChannelID: "c1", Format: "openai", BaseURL: "u",
		Models: map[string]string{"gpt-4o": ""}, Priority: 10, Weight: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateChannel 失败: %v", err)
	}
	if _, err := r.Select("gpt-4o"); err != nil {
		t.Fatalf("删除前应可选中: %v", err)
	}

	if err := svc.DeleteChannel(ctx, "c1"); err != nil {
		t.Fatalf("DeleteChannel 失败: %v", err)
	}
	if _, err := r.Select("gpt-4o"); !errors.Is(err, routing.ErrNoChannel) {
		t.Fatalf("删除后应选不到, 得到 %v", err)
	}
}

// TestServiceSetEnabledHotReload 验证禁用渠道后 router 立即排除它（Router 只索引 enabled 渠道）。
func TestServiceSetEnabledHotReload(t *testing.T) {
	svc, r := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateChannel(ctx, ChannelInput{
		ChannelID: "c1", Format: "openai", BaseURL: "u",
		Models: map[string]string{"gpt-4o": ""}, Priority: 10, Weight: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateChannel 失败: %v", err)
	}

	if _, err := svc.SetChannelEnabled(ctx, "c1", false); err != nil {
		t.Fatalf("SetChannelEnabled 失败: %v", err)
	}
	if _, err := r.Select("gpt-4o"); !errors.Is(err, routing.ErrNoChannel) {
		t.Fatalf("禁用后应选不到, 得到 %v", err)
	}
}

// TestServiceNilRouterNoPanic 验证 router 为 nil 时渠道写操作仍落库、不 panic。
func TestServiceNilRouterNoPanic(t *testing.T) {
	m := NewMemoryStore(store.NewMemoryStore(nil), nil)
	svc := NewService(m, nil, nil)
	ctx := context.Background()

	if _, err := svc.CreateChannel(ctx, ChannelInput{
		ChannelID: "c1", Format: "openai", BaseURL: "u", Weight: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("nil router 下 CreateChannel 应成功: %v", err)
	}
	ch, err := svc.Store().GetChannel(ctx, "c1")
	if err != nil || ch.ChannelID != "c1" {
		t.Fatalf("渠道应已落库: %+v err=%v", ch, err)
	}
	// ReloadChannels 在 nil router 下应为空操作、返回 nil。
	if err := svc.ReloadChannels(ctx); err != nil {
		t.Fatalf("nil router ReloadChannels 应返回 nil, 得到 %v", err)
	}
}
