package account

import (
	"errors"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLen 是密码最小长度（注册/改密时后端强校验，前端另有前置校验）。
const MinPasswordLen = 8

// MaxPasswordBytes 是 bcrypt 可安全处理的明文 UTF-8 字节上限。
const MaxPasswordBytes = 72

// ErrPasswordTooShort 表示密码长度不足。
var ErrPasswordTooShort = errors.New("account: 密码长度不足")

// ErrPasswordTooLong 表示密码超过 bcrypt 的 72 字节输入上限。
var ErrPasswordTooLong = errors.New("account: 密码长度超过 72 字节")

// HashPassword 用 bcrypt（默认 cost）哈希明文密码。绝不存明文、绝不用快哈希。
func HashPassword(plain string) (string, error) {
	if utf8.RuneCountInString(plain) < MinPasswordLen {
		return "", ErrPasswordTooShort
	}
	if len(plain) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
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
