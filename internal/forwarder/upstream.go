package forwarder

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"linapi/internal/routing"
)

const (
	maxUpstreamResponseBytes = 32 << 20
	maxSSERecordBytes        = 4 << 20
	upstreamStreamIdle       = 2 * time.Minute
)

var (
	errUpstreamResponseTooLarge = fmt.Errorf("forwarder: 上游响应超过 %d 字节", maxUpstreamResponseBytes)
	errSSERecordTooLarge        = fmt.Errorf("forwarder: SSE 记录超过 %d 字节", maxSSERecordBytes)
	errUpstreamStreamIdle       = errors.New("forwarder: 上游 SSE 空闲超时")
)

// upstreamClient 向上游供应商发起 HTTP 请求。
//
// 关于超时：非流式请求用带超时的 http.Client；流式（SSE）响应可能持续数分钟，
// 因此流式路径不设整体超时，仅靠请求 context（客户端断开即取消）与响应头超时兜底。
type upstreamClient struct {
	nonStream *http.Client
	stream    *http.Client
	policy    *UpstreamTargetPolicy
}

func newUpstreamClient() *upstreamClient {
	return newUpstreamClientWithPolicy(newDevelopmentTargetPolicy())
}

func newUpstreamClientWithPolicy(policy *UpstreamTargetPolicy) *upstreamClient {
	if policy == nil {
		policy = newDevelopmentTargetPolicy()
	}
	// 连接层参数：非流式与流式共用同一套 Transport 配置（连接复用、TLS 握手超时等）。
	transport := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DialContext:           policy.DialContext,
	}
	return &upstreamClient{
		nonStream: &http.Client{
			Transport:     transport,
			Timeout:       120 * time.Second,
			CheckRedirect: rejectRedirect,
		},
		stream: &http.Client{
			Transport:     transport,
			CheckRedirect: rejectRedirect,
			// 不设 Timeout：SSE 长回复可能持续数分钟，整体超时会中途掐断。
			// ResponseHeaderTimeout 保证上游迟迟不响应时不会无限等待。
		},
		policy: policy,
	}
}

// rejectRedirect 禁止 net/http 自动重放带凭证的 POST。尤其 Anthropic 使用的
// x-api-key 不属于 Go 默认会跨域剥离的标准 Authorization 头，自动 307/308
// 可能把密钥和完整请求体发送到非预期主机。
func rejectRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// upstreamResponse 是一次上游调用的结果，非流式与流式各用其一。
type upstreamResponse struct {
	// StatusCode 是上游 HTTP 状态码。
	StatusCode int
	// Body 是非流式响应体（流式时为 nil）。
	Body []byte
	// Stream 是流式响应体（非流式时为 nil），调用方负责关闭。
	Stream io.ReadCloser
	// Header 保存上游响应头；调用方只能通过安全允许列表转发。
	Header http.Header
}

// preparedUpstreamRequest 把完全本地的 URL/HTTP 请求构造与真实网络 I/O 分开。
// Forwarder 必须先 prepare 成功，再把 reservation 标成 in_flight，最后才 send。
type preparedUpstreamRequest struct {
	request *http.Request
	client  *http.Client
	stream  bool
}

func (u *upstreamClient) prepare(ctx context.Context, ch *routing.Channel, body []byte, stream bool) (*preparedUpstreamRequest, error) {
	url, err := u.policy.BuildURL(ch)
	if err != nil {
		return nil, fmt.Errorf("forwarder: 上游目标策略拒绝渠道 %q: %w", ch.ID, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("forwarder: 构造上游请求失败: %w", err)
	}
	applyAuthHeaders(req, ch)

	client := u.nonStream
	if stream {
		client = u.stream
	}
	return &preparedUpstreamRequest{request: req, client: client, stream: stream}, nil
}

// send 执行已经完成本地校验的请求。调用后任何错误都视为发送结果不确定，
// 不得自动退款或换渠道。
func (u *upstreamClient) send(prepared *preparedUpstreamRequest) (*upstreamResponse, error) {
	resp, err := prepared.client.Do(prepared.request)
	if err != nil {
		return nil, fmt.Errorf("forwarder: 调用上游失败: %w", err)
	}

	if prepared.stream {
		return &upstreamResponse{
			StatusCode: resp.StatusCode,
			Stream:     newIdleReadCloser(resp.Body, upstreamStreamIdle),
			Header:     resp.Header.Clone(),
		}, nil
	}

	defer resp.Body.Close()
	respBody, err := readAtMost(resp.Body, maxUpstreamResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("forwarder: 读取上游响应失败: %w", err)
	}
	return &upstreamResponse{StatusCode: resp.StatusCode, Body: respBody, Header: resp.Header.Clone()}, nil
}

func readAtMost(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errUpstreamResponseTooLarge
	}
	return body, nil
}

type idleReadCloser struct {
	io.ReadCloser
	timeout  time.Duration
	timedOut atomic.Bool
}

func newIdleReadCloser(rc io.ReadCloser, timeout time.Duration) io.ReadCloser {
	return &idleReadCloser{ReadCloser: rc, timeout: timeout}
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	if r.timedOut.Load() {
		return 0, errUpstreamStreamIdle
	}
	timer := time.AfterFunc(r.timeout, func() {
		r.timedOut.Store(true)
		_ = r.ReadCloser.Close()
	})
	n, err := r.ReadCloser.Read(p)
	_ = timer.Stop()
	if r.timedOut.Load() {
		return n, errUpstreamStreamIdle
	}
	return n, err
}

// applyAuthHeaders 按渠道格式设置鉴权与内容头。
// OpenAI 用 Authorization: Bearer；Anthropic 用 x-api-key + anthropic-version。
func applyAuthHeaders(req *http.Request, ch *routing.Channel) {
	req.Header.Set("Content-Type", "application/json")
	switch ch.Format {
	case routing.FormatAnthropic:
		req.Header.Set("x-api-key", ch.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default: // FormatOpenAI
		req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	}
}

// sseReader 按 SSE 记录边界（空行分隔）读取流式响应。
// 每次 Next 返回一条完整记录（含其内部的 event:/data: 行，不含分隔空行），
// 供适配器的 StreamDecoder 逐条解析为规范事件。
type sseReader struct {
	r         *bufio.Reader
	buf       bytes.Buffer
	firstLine bool
}

func newSSEReader(rc io.Reader) *sseReader {
	return &sseReader{r: bufio.NewReaderSize(rc, 16*1024), firstLine: true}
}

// Next 返回下一条 SSE 记录（不含末尾空行）。流结束返回 io.EOF。
func (s *sseReader) Next() ([]byte, error) {
	s.buf.Reset()
	for {
		line, err := s.readLine()
		if s.firstLine {
			line = bytes.TrimPrefix(line, []byte{0xEF, 0xBB, 0xBF})
			s.firstLine = false
		}
		// 空行 = 记录边界：已有累积内容则返回该记录。
		if len(line) == 0 && err == nil {
			if s.buf.Len() > 0 {
				return append([]byte(nil), s.buf.Bytes()...), nil
			}
			// 连续空行，跳过。
			continue
		}
		if len(line) > 0 {
			extra := len(line)
			if s.buf.Len() > 0 {
				extra++
			}
			if s.buf.Len()+extra > maxSSERecordBytes {
				return nil, errSSERecordTooLarge
			}
			if s.buf.Len() > 0 {
				s.buf.WriteByte('\n')
			}
			s.buf.Write(line)
		}
		if err != nil {
			// 流结束前若还有残留记录，先返回它，下次调用再报 EOF。
			if s.buf.Len() > 0 {
				return append([]byte(nil), s.buf.Bytes()...), nil
			}
			return nil, err
		}
	}
}

// readLine 按 WHATWG 允许的 LF、CRLF 或裸 CR 读取一行，不返回行结束符。
func (s *sseReader) readLine() ([]byte, error) {
	line := make([]byte, 0, 256)
	for {
		b, err := s.r.ReadByte()
		if err != nil {
			return line, err
		}
		switch b {
		case '\n':
			return line, nil
		case '\r':
			if next, err := s.r.Peek(1); err == nil && next[0] == '\n' {
				_, _ = s.r.ReadByte()
			}
			return line, nil
		default:
			if len(line) >= maxSSERecordBytes {
				return nil, errSSERecordTooLarge
			}
			line = append(line, b)
		}
	}
}
