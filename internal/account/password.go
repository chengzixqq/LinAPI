package account

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLen 是密码最小长度（注册/改密时后端强校验，前端另有前置校验）。
const MinPasswordLen = 8

// ErrPasswordTooShort 表示密码长度不足。
var ErrPasswordTooShort = errors.New("account: 密码长度不足")

// HashPassword 用 bcrypt（默认 cost）哈希明文密码。绝不存明文、绝不用快哈希。
func HashPassword(plain string) (string, error) {
	if len(plain) < MinPasswordLen {
		return "", ErrPasswordTooShort
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword 校验明文与 bcrypt 哈希是否匹配。
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
