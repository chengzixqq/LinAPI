package forwarder

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"linapi/internal/routing"
)

// upstreamClient 向上游供应商发起 HTTP 请求。
//
// 关于超时：非流式请求用带超时的 http.Client；流式（SSE）响应可能持续数分钟，
// 因此流式路径不设整体超时，仅靠请求 context（客户端断开即取消）与响应头超时兜底。
type upstreamClient struct {
	nonStream *http.Client
	stream    *http.Client
}

func newUpstreamClient() *upstreamClient {
	// 连接层参数：非流式与流式共用同一套 Transport 配置（连接复用、TLS 握手超时等）。
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &upstreamClient{
		nonStream: &http.Client{
			Transport: transport,
			Timeout:   120 * time.Second,
		},
		stream: &http.Client{
			Transport: transport,
			// 不设 Timeout：SSE 长回复可能持续数分钟，整体超时会中途掐断。
			// ResponseHeaderTimeout 保证上游迟迟不响应时不会无限等待。
		},
	}
}

// upstreamResponse 是一次上游调用的结果，非流式与流式各用其一。
type upstreamResponse struct {
	// StatusCode 是上游 HTTP 状态码。
	StatusCode int
	// Body 是非流式响应体（流式时为 nil）。
	Body []byte
	// Stream 是流式响应体（非流式时为 nil），调用方负责关闭。
	Stream io.ReadCloser
}

// do 向指定渠道发起请求。stream 决定走流式还是非流式客户端。
// body 是已按渠道格式（BuildRequest）构造好的请求体。
func (u *upstreamClient) do(ctx context.Context, ch *routing.Channel, body []byte, stream bool) (*upstreamResponse, error) {
	url := buildUpstreamURL(ch)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("forwarder: 构造上游请求失败: %w", err)
	}
	applyAuthHeaders(req, ch)

	client := u.nonStream
	if stream {
		client = u.stream
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("forwarder: 调用上游失败: %w", err)
	}

	if stream {
		return &upstreamResponse{StatusCode: resp.StatusCode, Stream: resp.Body}, nil
	}

	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("forwarder: 读取上游响应失败: %w", err)
	}
	return &upstreamResponse{StatusCode: resp.StatusCode, Body: respBody}, nil
}

// buildUpstreamURL 按渠道格式拼出上游端点路径。
// BaseURL 允许带或不带尾斜杠；不同格式端点路径不同。
func buildUpstreamURL(ch *routing.Channel) string {
	base := strings.TrimRight(ch.BaseURL, "/")
	switch ch.Format {
	case routing.FormatAnthropic:
		return base + "/v1/messages"
	default: // FormatOpenAI
		return base + "/v1/chat/completions"
	}
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
	r   *bufio.Reader
	buf bytes.Buffer
}

func newSSEReader(rc io.Reader) *sseReader {
	return &sseReader{r: bufio.NewReaderSize(rc, 16*1024)}
}

// Next 返回下一条 SSE 记录（不含末尾空行）。流结束返回 io.EOF。
func (s *sseReader) Next() ([]byte, error) {
	s.buf.Reset()
	for {
		line, err := s.r.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\r\n")
			// 空行 = 记录边界：已有累积内容则返回该记录。
			if len(trimmed) == 0 {
				if s.buf.Len() > 0 {
					return append([]byte(nil), s.buf.Bytes()...), nil
				}
				// 连续空行，跳过。
				if err != nil {
					return nil, err
				}
				continue
			}
			if s.buf.Len() > 0 {
				s.buf.WriteByte('\n')
			}
			s.buf.Write(trimmed)
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
