package forwarder

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"linapi/internal/config"
	"linapi/internal/routing"
)

var ErrUpstreamNotDialed = errors.New("forwarder: 上游目标未拨号")

type targetResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type targetDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type upstreamTargetRule struct {
	allowHTTP bool
	cidrs     []netip.Prefix
}

// UpstreamTargetPolicy 同时执行 URL 静态校验与拨号期地址校验。strict=false 仅供本地
// 测试/开发，仍拒绝 userinfo/query/fragment 等路径绕过；release 使用 strict=true。
type UpstreamTargetPolicy struct {
	strict   bool
	rules    map[string]upstreamTargetRule // 规范 host:port
	resolver targetResolver
	dialer   targetDialer
}

func NewUpstreamTargetPolicy(cfg config.UpstreamConfig, strict bool) (*UpstreamTargetPolicy, error) {
	p := &UpstreamTargetPolicy{
		strict:   strict,
		rules:    make(map[string]upstreamTargetRule, len(cfg.TargetRules)),
		resolver: net.DefaultResolver,
		dialer:   &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
	}
	for _, raw := range cfg.TargetRules {
		authority, err := normalizeAuthority(raw.Authority)
		if err != nil {
			return nil, fmt.Errorf("forwarder: 上游目标规则 authority %q 无效: %w", raw.Authority, err)
		}
		if _, exists := p.rules[authority]; exists {
			return nil, fmt.Errorf("forwarder: 上游目标规则 %q 重复", authority)
		}
		rule := upstreamTargetRule{allowHTTP: raw.AllowHTTP}
		for _, rawCIDR := range raw.AllowedCIDRs {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(rawCIDR))
			if err != nil {
				return nil, fmt.Errorf("forwarder: authority %q 的 CIDR %q 无效: %w", authority, rawCIDR, err)
			}
			rule.cidrs = append(rule.cidrs, prefix.Masked())
		}
		p.rules[authority] = rule
	}
	return p, nil
}

func newDevelopmentTargetPolicy() *UpstreamTargetPolicy {
	p, err := NewUpstreamTargetPolicy(config.UpstreamConfig{}, false)
	if err != nil {
		panic(err)
	}
	return p
}

func (p *UpstreamTargetPolicy) ValidateChannels(channels []*routing.Channel) error {
	for _, ch := range channels {
		if ch == nil || !ch.Enabled {
			continue
		}
		if err := p.ValidateChannel(ch); err != nil {
			return fmt.Errorf("渠道 %q: %w", ch.ID, err)
		}
	}
	return nil
}

func (p *UpstreamTargetPolicy) ValidateChannel(ch *routing.Channel) error {
	if ch == nil {
		return fmt.Errorf("上游渠道为空")
	}
	_, _, err := p.parseBaseURL(ch.BaseURL)
	return err
}

// BuildURL 结构化追加供应商端点，避免 base_url 的 query/fragment 吞掉固定路径。
func (p *UpstreamTargetPolicy) BuildURL(ch *routing.Channel) (string, error) {
	u, _, err := p.parseBaseURL(ch.BaseURL)
	if err != nil {
		return "", err
	}
	endpoint := "/v1/chat/completions"
	if ch.Format == routing.FormatAnthropic {
		endpoint = "/v1/messages"
	}
	u.Path = strings.TrimRight(u.Path, "/") + endpoint
	u.RawPath = ""
	return u.String(), nil
}

func (p *UpstreamTargetPolicy) parseBaseURL(raw string) (*url.URL, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", fmt.Errorf("base_url 解析失败: %w", err)
	}
	if !u.IsAbs() || u.Host == "" || u.Opaque != "" {
		return nil, "", fmt.Errorf("base_url 必须是绝对 HTTP(S) URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, "", fmt.Errorf("base_url scheme 只允许 https/http")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, "", fmt.Errorf("base_url 不允许 userinfo、query 或 fragment")
	}
	checkPath := strings.TrimSuffix(u.Path, "/")
	if u.RawPath != "" || strings.Contains(u.EscapedPath(), "%") ||
		(checkPath != "" && path.Clean(checkPath) != checkPath) || strings.Contains(u.Path, "//") {
		return nil, "", fmt.Errorf("base_url path 必须是无转义的规范路径")
	}
	if strings.Contains(u.Hostname(), "%") {
		return nil, "", fmt.Errorf("base_url 不允许 IPv6 zone")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	authority, err := normalizeHostPort(u.Hostname(), port)
	if err != nil {
		return nil, "", err
	}
	rule := p.rules[authority]
	if p.strict && u.Scheme != "https" && !rule.allowHTTP {
		return nil, "", fmt.Errorf("release 模式只允许 HTTPS；%q 未显式允许 HTTP", authority)
	}
	return u, authority, nil
}

func normalizeAuthority(authority string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(authority))
	if err != nil {
		return "", fmt.Errorf("必须使用 host:port: %w", err)
	}
	return normalizeHostPort(host, port)
}

func normalizeHostPort(host, port string) (string, error) {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || strings.Contains(host, "%") {
		return "", fmt.Errorf("host 无效")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("port 无效")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		host = addr.Unmap().String()
	}
	return net.JoinHostPort(host, strconv.Itoa(portNumber)), nil
}

func (p *UpstreamTargetPolicy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if !p.strict {
		return p.dialer.DialContext(ctx, network, address)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: 地址 %q 无效", ErrUpstreamNotDialed, address)
	}
	authority, err := normalizeHostPort(host, port)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstreamNotDialed, err)
	}
	var addrs []netip.Addr
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		addrs = []netip.Addr{addr.Unmap()}
	} else {
		addrs, err = p.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("%w: DNS 解析 %q 失败: %v", ErrUpstreamNotDialed, host, err)
		}
	}
	var dialErrors []error
	allowedCount := 0
	for _, addr := range addrs {
		addr = addr.Unmap()
		if !p.ipAllowed(authority, addr) {
			continue
		}
		allowedCount++
		conn, err := p.dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		dialErrors = append(dialErrors, err)
	}
	if allowedCount == 0 {
		return nil, fmt.Errorf("%w: %q 只解析到被策略阻止的地址", ErrUpstreamNotDialed, authority)
	}
	return nil, fmt.Errorf("%w: %q 连接失败: %v", ErrUpstreamNotDialed, authority, errors.Join(dialErrors...))
}

func (p *UpstreamTargetPolicy) ipAllowed(authority string, addr netip.Addr) bool {
	if isPublicUpstreamIP(addr) {
		return true
	}
	rule, ok := p.rules[authority]
	if !ok {
		return false
	}
	for _, prefix := range rule.cidrs {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func isPublicUpstreamIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsMulticast() {
		return false
	}
	for _, prefix := range reservedUpstreamPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

var reservedUpstreamPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}
