package forwarder

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"linapi/internal/config"
	"linapi/internal/db"
	"linapi/internal/routing"
)

func TestChannelsFromConfig(t *testing.T) {
	cfgs := []config.ChannelConfig{
		{
			ID:       "c1",
			Name:     "主渠道",
			Format:   "openai",
			BaseURL:  "https://api.openai.com",
			APIKey:   "sk-up",
			Models:   map[string]string{"gpt-4o": "gpt-4o-2024-08-06"},
			Priority: 10,
			Weight:   5,
			Enabled:  true,
		},
	}
	got := ChannelsFromConfig(cfgs)
	if len(got) != 1 {
		t.Fatalf("应转换 1 个渠道，得 %d", len(got))
	}
	c := got[0]
	if c.ID != "c1" || c.Format != routing.FormatOpenAI || c.Priority != 10 || c.Weight != 5 {
		t.Errorf("渠道字段映射错误: %+v", c)
	}
	if c.UpstreamModel("gpt-4o") != "gpt-4o-2024-08-06" {
		t.Errorf("上游模型映射错误: %s", c.UpstreamModel("gpt-4o"))
	}
}

func TestChannelsFromDB(t *testing.T) {
	rows := []db.ListEnabledChannelsRow{
		{
			ChannelID: "db1",
			Name:      "库渠道",
			Format:    "anthropic",
			BaseURL:   "https://api.anthropic.com",
			ApiKey:    "sk-ant",
			Models:    []byte(`{"claude-3-5-sonnet":"claude-3-5-sonnet-20241022"}`),
			Priority:  20,
			Weight:    3,
			Enabled:   true,
		},
	}
	got, err := ChannelsFromDB(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("应转换 1 个渠道，得 %d", len(got))
	}
	c := got[0]
	if c.ID != "db1" || c.Format != routing.FormatAnthropic {
		t.Errorf("渠道字段映射错误: %+v", c)
	}
	if c.UpstreamModel("claude-3-5-sonnet") != "claude-3-5-sonnet-20241022" {
		t.Errorf("上游模型映射错误: %s", c.UpstreamModel("claude-3-5-sonnet"))
	}
}

// TestChannelsFromDBBadJSON 确认坏的 models JSON 会报错而非静默污染路由。
func TestChannelsFromDBBadJSON(t *testing.T) {
	rows := []db.ListEnabledChannelsRow{
		{ChannelID: "bad", Format: "openai", Models: []byte(`{不是JSON`)},
	}
	if _, err := ChannelsFromDB(rows); err == nil {
		t.Fatal("坏 JSON 应返回错误")
	}
}

// TestChannelsFromDBEmptyModels 确认空 models 列不报错（得到空映射）。
func TestChannelsFromDBEmptyModels(t *testing.T) {
	rows := []db.ListEnabledChannelsRow{
		{ChannelID: "empty", Format: "openai", Models: nil},
	}
	got, err := ChannelsFromDB(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Models == nil {
		t.Fatalf("空 models 应得到非 nil 空映射: %+v", got)
	}
}

func TestSSEReader(t *testing.T) {
	// 三条记录：单 data 行、event+data 两行、结束标记。记录间用空行分隔。
	raw := "data: {\"a\":1}\n\n" +
		"event: delta\ndata: {\"b\":2}\n\n" +
		"data: [DONE]\n\n"
	r := newSSEReader(strings.NewReader(raw))

	want := []string{
		`data: {"a":1}`,
		"event: delta\ndata: {\"b\":2}",
		"data: [DONE]",
	}
	for i, w := range want {
		rec, err := r.Next()
		if err != nil {
			t.Fatalf("记录 %d 读取失败: %v", i, err)
		}
		if string(rec) != w {
			t.Errorf("记录 %d = %q，期望 %q", i, rec, w)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("末尾应返回 EOF，得 %v", err)
	}
}

// TestSSEReaderNoTrailingBlank 确认流末尾缺少空行时仍能取出最后一条记录。
func TestSSEReaderNoTrailingBlank(t *testing.T) {
	r := newSSEReader(strings.NewReader("data: {\"x\":1}"))
	rec, err := r.Next()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(rec) != `data: {"x":1}` {
		t.Errorf("记录 = %q", rec)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("应返回 EOF，得 %v", err)
	}
}

func TestSSEReaderAcceptsBOMAndBareCR(t *testing.T) {
	r := newSSEReader(strings.NewReader("\uFEFFevent: chunk\rdata: {\"x\":1}\r\rdata: [DONE]\r\r"))
	want := []string{"event: chunk\ndata: {\"x\":1}", "data: [DONE]"}
	for i, expected := range want {
		rec, err := r.Next()
		if err != nil {
			t.Fatalf("记录 %d 读取失败: %v", i, err)
		}
		if string(rec) != expected {
			t.Fatalf("记录 %d = %q, want %q", i, rec, expected)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("末尾应返回 EOF，得 %v", err)
	}
}

func TestReadAtMostRejectsOversizedResponse(t *testing.T) {
	if _, err := readAtMost(strings.NewReader("1234"), 3); !errors.Is(err, errUpstreamResponseTooLarge) {
		t.Fatalf("超大响应应被拒绝，得到 %v", err)
	}
}

func TestIdleReadCloserTimesOutStalledStream(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	r := newIdleReadCloser(reader, 20*time.Millisecond)
	start := time.Now()
	_, err := r.Read(make([]byte, 1))
	if !errors.Is(err, errUpstreamStreamIdle) {
		t.Fatalf("停滞流应返回空闲超时，得到 %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("空闲超时未及时解除阻塞: %s", elapsed)
	}
}

func TestSSEReaderRejectsOversizedRecord(t *testing.T) {
	r := newSSEReader(strings.NewReader("data: " + strings.Repeat("x", maxSSERecordBytes) + "\n\n"))
	if _, err := r.Next(); !errors.Is(err, errSSERecordTooLarge) {
		t.Fatalf("超大 SSE 记录应被拒绝，得到 %v", err)
	}
}
