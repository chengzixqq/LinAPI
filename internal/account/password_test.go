package account

import "testing"

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
