package adapter

import (
	"testing"

	"linapi/internal/canonical"
)

// mockAdapter 是用于测试注册表的最小假适配器。
type mockAdapter struct{ name string }

func (m *mockAdapter) Name() string                                      { return m.name }
func (m *mockAdapter) ParseRequest([]byte) (*canonical.Request, error)   { return nil, nil }
func (m *mockAdapter) BuildRequest(*canonical.Request) ([]byte, error)   { return nil, nil }
func (m *mockAdapter) ParseResponse([]byte) (*canonical.Response, error) { return nil, nil }
func (m *mockAdapter) BuildResponse(*canonical.Response) ([]byte, error) { return nil, nil }
func (m *mockAdapter) NewStreamDecoder() StreamDecoder                   { return nil }
func (m *mockAdapter) NewStreamEncoder() StreamEncoder                   { return nil }

// resetRegistry 清空全局注册表，供各测试用例独立运行。
func resetRegistry() {
	registry.Lock()
	defer registry.Unlock()
	registry.adapters = make(map[string]Adapter)
}

func TestRegisterAndGet(t *testing.T) {
	resetRegistry()

	Register(&mockAdapter{name: "openai"})

	got, ok := Get("openai")
	if !ok {
		t.Fatal("期望能取到已注册的适配器 openai，实际取不到")
	}
	if got.Name() != "openai" {
		t.Fatalf("适配器名不符：期望 openai，实际 %q", got.Name())
	}

	if _, ok := Get("nonexistent"); ok {
		t.Fatal("未注册的适配器不应被取到")
	}
}

func TestMustGet(t *testing.T) {
	resetRegistry()
	Register(&mockAdapter{name: "anthropic"})

	if _, err := MustGet("anthropic"); err != nil {
		t.Fatalf("MustGet 已注册适配器不应报错：%v", err)
	}
	if _, err := MustGet("missing"); err == nil {
		t.Fatal("MustGet 未注册适配器应返回错误")
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	resetRegistry()
	Register(&mockAdapter{name: "dup"})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("重复注册同名适配器应 panic")
		}
	}()
	Register(&mockAdapter{name: "dup"}) // 应触发 panic
}

func TestRegisterEmptyNamePanics(t *testing.T) {
	resetRegistry()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("注册空名适配器应 panic")
		}
	}()
	Register(&mockAdapter{name: ""})
}

func TestNamesSorted(t *testing.T) {
	resetRegistry()
	Register(&mockAdapter{name: "openai"})
	Register(&mockAdapter{name: "anthropic"})
	Register(&mockAdapter{name: "gemini"})

	names := Names()
	want := []string{"anthropic", "gemini", "openai"}
	if len(names) != len(want) {
		t.Fatalf("适配器数量不符：期望 %d，实际 %d", len(want), len(names))
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Names() 未按字典序返回：期望 %v，实际 %v", want, names)
		}
	}
}
