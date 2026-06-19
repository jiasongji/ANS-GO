package main

import (
	"encoding/base64"

	"golang.org/x/crypto/bcrypt"
)

// genBrypt 生成 bcrypt 哈希（DefaultCost=10）
func genBcrypt(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// cmpBcrypt 校验明文与哈希是否匹配
func cmpBcrypt(hash, plain string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// validSS2022Key 校验 SS-2022 密钥：base64 解码后字节数是否等于要求
// aes-128-gcm -> 16 bytes, aes-256-gcm -> 32 bytes
func validSS2022Key(b64 string, wantBytes int) bool {
	// 标准和 urlsafe 两种 base64 都尝试
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(b64); err == nil && len(raw) == wantBytes {
			return true
		}
	}
	return false
}
