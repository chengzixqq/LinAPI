package adapter

import (
	"bytes"
	"testing"
)

func TestSSEData(t *testing.T) {
	raw := []byte(": keepalive\nevent: chunk\nid: 7\ndata: {\"a\":\ndata: 1}\nretry: 1000")
	got, ok := SSEData(raw)
	if !ok || !bytes.Equal(got, []byte("{\"a\":\n1}")) {
		t.Fatalf("多 data 行提取错误: ok=%v got=%q", ok, got)
	}
	if got, ok := SSEData([]byte(": keepalive")); ok || got != nil {
		t.Fatalf("纯注释不得伪造 data: ok=%v got=%q", ok, got)
	}
}
