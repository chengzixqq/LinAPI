package account

import (
	"strings"
	"testing"
)

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatalf("HashPassword 失败: %v", err)
	}
	if hash == "s3cret-pw" {
		t.Fatal("哈希不得等于明文")
	}
	if !CheckPassword(hash, "s3cret-pw") {
		t.Error("正确密码应校验通过")
	}
	if CheckPassword(hash, "wrong-pw") {
		t.Error("错误密码不应通过")
	}
}

func TestHashPasswordTooShort(t *testing.T) {
	if _, err := HashPassword("short"); err != ErrPasswordTooShort {
		t.Fatalf("短密码应返回 ErrPasswordTooShort, 得到 %v", err)
	}
}

func TestHashPasswordCountsUnicodeCharacters(t *testing.T) {
	if _, err := HashPassword("中文中文中文中"); err != ErrPasswordTooShort {
		t.Fatalf("7 个字符应过短，得到 %v", err)
	}
	if _, err := HashPassword("中文中文中文中文"); err != nil {
		t.Fatalf("8 个字符且不超过 72 字节应可用，得到 %v", err)
	}
}

func TestHashPasswordRejectsBcryptOverflow(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes+1)); err != ErrPasswordTooLong {
		t.Fatalf("超过 72 字节应返回 ErrPasswordTooLong，得到 %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes)); err != nil {
		t.Fatalf("72 字节边界应可用，得到 %v", err)
	}
}
