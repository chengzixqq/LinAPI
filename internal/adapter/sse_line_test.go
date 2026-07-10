package adapter

import (
	"bytes"
	"testing"
)

func TestSSEDataAcceptsBOMAndBareCR(t *testing.T) {
	got, ok := SSEData([]byte("\xEF\xBB\xBFevent: chunk\rdata: first\rdata: second"))
	if !ok || !bytes.Equal(got, []byte("first\nsecond")) {
		t.Fatalf("BOM/裸 CR 记录解析错误: ok=%v got=%q", ok, got)
	}
}
