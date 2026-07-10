package adapter

import "bytes"

// SSEData 按 WHATWG SSE 记录语义提取 data 字段。多个 data 行以换行拼接；
// comment、event、id、retry 等字段不进入 payload。第二个返回值表示记录是否
// 含 data 字段。为兼容适配器单测与少数非标准上游，也接受裸 JSON / [DONE]。
func SSEData(raw []byte) ([]byte, bool) {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	raw = bytes.ReplaceAll(raw, []byte("\r"), []byte("\n"))
	var parts [][]byte
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if !found || !bytes.Equal(field, []byte("data")) {
			continue
		}
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		parts = append(parts, append([]byte(nil), value...))
	}
	if len(parts) > 0 {
		return bytes.Join(parts, []byte("\n")), true
	}

	trimmed := bytes.TrimSpace(raw)
	if bytes.HasPrefix(trimmed, []byte("{")) || bytes.Equal(trimmed, []byte("[DONE]")) {
		return trimmed, true
	}
	return nil, false
}
