package main

import (
	"encoding/base64"
	"strings"

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

// decodeSS2022Key 尝试用标准/urlsafe、有 padding/无 padding 四种形式解析 SS-2022 密钥。
func decodeSS2022Key(b64 string) ([]byte, bool) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(b64); err == nil {
			return raw, true
		}
	}
	return nil, false
}

// validSS2022Key 校验 SS-2022 密钥：base64 解码后字节数是否等于要求
// aes-128-gcm -> 16 bytes, aes-256-gcm -> 32 bytes
func validSS2022Key(b64 string, wantBytes int) bool {
	raw, ok := decodeSS2022Key(b64)
	return ok && len(raw) == wantBytes
}

func ss2022KeyError(method, key string) string {
	wantBytes := 0
	switch {
	case method == "" || strings.HasPrefix(method, "2022-blake3-aes-128"):
		wantBytes = 16
	case strings.HasPrefix(method, "2022-blake3-aes-256"):
		wantBytes = 32
	default:
		return ""
	}
	raw, ok := decodeSS2022Key(key)
	if !ok {
		return "密钥格式错误：当前密钥不是合法 base64；" + method + " 需填写 base64(" + itoa(wantBytes) + "字节) 密钥；可点击 🎲 随机生成后保存"
	}
	if len(raw) != wantBytes {
		return "密钥长度错误：" + method + " 需 base64(" + itoa(wantBytes) + "字节) 的密钥；当前解码后为 " + itoa(len(raw)) + " 字节"
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
