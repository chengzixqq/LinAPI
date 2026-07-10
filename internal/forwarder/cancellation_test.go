package forwarder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"linapi/internal/routing"
)

func TestClientCancellationDoesNotAffectBreakersOrRetry(t *testing.T) {
	started := make(chan struct{}, 1)
	releaseUpstream := make(chan struct{})
	defer close(releaseUpstream)
	var firstCalls atomic.Int32
	first := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-releaseUpstream:
		}
	})

	var secondCalls atomic.Int32
	second := mockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		http.Error(w, "不应请求后备渠道", http.StatusInternalServerError)
	})

	env := newTestEnvWithBreakerConfig(t, []*routing.Channel{
		openAIChannel("first", first.URL, 20),
		openAIChannel("second", second.URL, 10),
	}, 1_000_000, routing.BreakerConfig{
		FailureThreshold:  1,
		CooldownPeriod:    time.Minute,
		HalfOpenMaxProbes: 1,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(openAIChatReq)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		env.engine.ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("首个上游请求未开始")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端取消后转发请求未及时结束")
	}

	if got := firstCalls.Load(); got != 1 {
		t.Fatalf("首选渠道调用次数 = %d, 期望 1", got)
	}
	if got := secondCalls.Load(); got != 0 {
		t.Fatalf("客户端取消后不应重试后备渠道, 实际调用 %d 次", got)
	}

	candidates, err := env.router.Select("gpt-4o")
	if err != nil {
		t.Fatalf("取消后选择渠道失败: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("取消后两个渠道都应保持可用, 实际候选数 %d", len(candidates))
	}
	for _, candidate := range candidates {
		if state := candidate.Breaker.State(); state != routing.StateClosed {
			t.Errorf("渠道 %s 的熔断状态 = %s, 期望 closed", candidate.Channel.ID, state)
		}
	}
}
