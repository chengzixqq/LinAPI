package forwarder

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"

	"linapi/internal/config"
	"linapi/internal/routing"
)

type sequenceResolver struct {
	mu      sync.Mutex
	answers [][]netip.Addr
}

func (r *sequenceResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.answers) == 0 {
		return nil, errors.New("no answer")
	}
	out := r.answers[0]
	r.answers = r.answers[1:]
	return out, nil
}

type recordingDialer struct {
	mu        sync.Mutex
	addresses []string
}

func (d *recordingDialer) DialContext(_ context.Context, _ string, address string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addresses = append(d.addresses, address)
	return nil, errors.New("test dial stopped")
}

func TestTargetPolicyRejectsURLConfusionAndPlainHTTP(t *testing.T) {
	p, err := NewUpstreamTargetPolicy(config.UpstreamConfig{}, true)
	if err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"http://api.example.com",
		"https://user:pass@api.example.com",
		"https://api.example.com/base?ignored=",
		"https://api.example.com/base#fragment",
		"https://api.example.com/%2e%2e/private",
	}
	for _, raw := range bad {
		if err := p.ValidateChannel(&routing.Channel{ID: "bad", BaseURL: raw}); err == nil {
			t.Errorf("应拒绝 base_url %q", raw)
		}
	}
}

func TestTargetPolicyAllowsExactPrivateCIDRException(t *testing.T) {
	p, err := NewUpstreamTargetPolicy(config.UpstreamConfig{TargetRules: []config.UpstreamTargetRuleConfig{{
		Authority: "llm.internal:8080", AllowHTTP: true, AllowedCIDRs: []string{"10.20.0.0/24"},
	}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	ch := &routing.Channel{ID: "private", BaseURL: "http://llm.internal:8080/base/", Format: routing.FormatOpenAI}
	if err := p.ValidateChannel(ch); err != nil {
		t.Fatalf("精确例外应通过静态校验: %v", err)
	}
	got, err := p.BuildURL(ch)
	if err != nil || got != "http://llm.internal:8080/base/v1/chat/completions" {
		t.Fatalf("BuildURL=%q err=%v", got, err)
	}
	if !p.ipAllowed("llm.internal:8080", netip.MustParseAddr("10.20.0.8")) ||
		p.ipAllowed("llm.internal:8080", netip.MustParseAddr("10.21.0.8")) {
		t.Fatal("私网例外必须严格受 CIDR 约束")
	}
}

func TestTargetPolicyBlocksDNSRebindingBeforeDial(t *testing.T) {
	p, err := NewUpstreamTargetPolicy(config.UpstreamConfig{}, true)
	if err != nil {
		t.Fatal(err)
	}
	p.resolver = &sequenceResolver{answers: [][]netip.Addr{
		{netip.MustParseAddr("8.8.8.8")},
		{netip.MustParseAddr("127.0.0.1")},
	}}
	dialer := &recordingDialer{}
	p.dialer = dialer

	for i := 0; i < 2; i++ {
		if _, err := p.DialContext(context.Background(), "tcp", "rebind.example:443"); !errors.Is(err, ErrUpstreamNotDialed) {
			t.Fatalf("拨号 %d 应返回未发送错误，得到 %v", i+1, err)
		}
	}
	if len(dialer.addresses) != 1 || dialer.addresses[0] != "8.8.8.8:443" {
		t.Fatalf("第二次私网解析不得进入底层 dialer: %v", dialer.addresses)
	}
}

func TestPublicIPClassificationRejectsMappedLoopbackAndReservedRanges(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "::ffff:127.0.0.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "2001:db8::1"} {
		if isPublicUpstreamIP(netip.MustParseAddr(raw)) {
			t.Errorf("%s 不应视为公共上游地址", raw)
		}
	}
	if !isPublicUpstreamIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("公共地址被误拒绝")
	}
}
