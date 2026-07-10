package forwarder

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"linapi/internal/billing"
	"linapi/internal/middleware"
	"linapi/internal/routing"
	"linapi/internal/store"
)

type failingConsumptionLedger struct {
	mu          sync.Mutex
	recordCalls int
	refundCalls int
}

type markFailureLedger struct {
	mu           sync.Mutex
	releaseCalls int
	refundCalls  int
}

func (l *markFailureLedger) Reserve(context.Context, billing.Reservation) (bool, error) {
	return true, nil
}
func (l *markFailureLedger) MarkInFlight(context.Context, string, string) error {
	return errors.New("injected mark-in-flight failure")
}
func (l *markFailureLedger) ReleaseAttempt(context.Context, string) error {
	l.mu.Lock()
	l.releaseCalls++
	l.mu.Unlock()
	return nil
}
func (l *markFailureLedger) RecordConsumption(context.Context, billing.Consumption) error {
	return errors.New("unexpected consumption")
}
func (l *markFailureLedger) Finalize(context.Context, string) error { return nil }
func (l *markFailureLedger) Refund(context.Context, string) error {
	l.mu.Lock()
	l.refundCalls++
	l.mu.Unlock()
	return nil
}
func (l *markFailureLedger) Recover(context.Context) error { return nil }

func (l *markFailureLedger) counts() (release, refund int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.releaseCalls, l.refundCalls
}

func (l *failingConsumptionLedger) Reserve(context.Context, billing.Reservation) (bool, error) {
	return true, nil
}
func (l *failingConsumptionLedger) MarkInFlight(context.Context, string, string) error { return nil }
func (l *failingConsumptionLedger) ReleaseAttempt(context.Context, string) error       { return nil }
func (l *failingConsumptionLedger) RecordConsumption(context.Context, billing.Consumption) error {
	l.mu.Lock()
	l.recordCalls++
	l.mu.Unlock()
	return errors.New("injected ledger failure")
}
func (l *failingConsumptionLedger) Finalize(context.Context, string) error { return nil }
func (l *failingConsumptionLedger) Refund(context.Context, string) error {
	l.mu.Lock()
	l.refundCalls++
	l.mu.Unlock()
	return nil
}
func (l *failingConsumptionLedger) Recover(context.Context) error { return nil }

func (l *failingConsumptionLedger) counts() (record, refund int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.recordCalls, l.refundCalls
}

// TestSuccessfulUpstreamLedgerFailureDoesNotRefund 覆盖 AUD-P0-03：上游成功后，
// 即使本地持久化 consumption 失败，也必须保留预授权，绝不能把真实消费全额退回。
func TestSuccessfulUpstreamLedgerFailureDoesNotRefund(t *testing.T) {
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, openAIChatResp)
	})
	st := store.NewMemoryStore([]store.KeySeed{{
		APIKey: "sk-test", KeyID: "k", UserID: "u", Enabled: true, InitialBalance: 1_000_000,
	}})
	ledger := &failingConsumptionLedger{}
	pricing := billing.NewPricing(nil, 1_000_000, 2_000_000)
	bill := billing.New(pricing, ledger, 1)
	fwd := New(routing.NewRouter([]*routing.Channel{openAIChannel("c1", up.URL, 1)}, routing.DefaultBreakerConfig()),
		bill, slog.New(slog.NewTextHandler(io.Discard, nil)))

	engine := gin.New()
	engine.Use(middleware.Auth(st))
	engine.POST("/v1/chat/completions", fwd.Handler("openai"))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(openAIChatReq))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("上游响应仍应交付客户端，status=%d body=%s", w.Code, w.Body.String())
	}
	records, refunds := ledger.counts()
	if records != 3 || refunds != 0 {
		t.Fatalf("结算失败后不得退款: record=%d refund=%d", records, refunds)
	}
}

func TestMarkInFlightFailureBeforeSendIsReleasedAndRefunded(t *testing.T) {
	var upstreamCalls int
	up := mockUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		_, _ = io.WriteString(w, openAIChatResp)
	})
	st := store.NewMemoryStore([]store.KeySeed{{
		APIKey: "sk-test", KeyID: "k", UserID: "u", Enabled: true, InitialBalance: 1_000_000,
	}})
	ledger := &markFailureLedger{}
	bill := billing.New(billing.NewPricing(nil, 1_000_000, 2_000_000), ledger, 1)
	fwd := New(routing.NewRouter([]*routing.Channel{openAIChannel("c1", up.URL, 1)}, routing.DefaultBreakerConfig()),
		bill, slog.New(slog.NewTextHandler(io.Discard, nil)))

	engine := gin.New()
	engine.Use(middleware.Auth(st))
	engine.POST("/v1/chat/completions", fwd.Handler("openai"))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(openAIChatReq))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("MarkInFlight 失败后不得发送上游请求，calls=%d", upstreamCalls)
	}
	releases, refunds := ledger.counts()
	if releases != 1 || refunds != 1 {
		t.Fatalf("发送前失败应先释放 attempt 再退款: release=%d refund=%d", releases, refunds)
	}
}
