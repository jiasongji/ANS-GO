package main

import "golang.org/x/crypto/bcrypt"

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
