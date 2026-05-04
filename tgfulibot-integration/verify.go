// Package license 是给 TGfulibot 主项目内嵌的 license 校验代码。
//
// 直接拷贝整个文件到 TGfulibot 项目的 backend/internal/license/verify.go。
// 同时把 master_public_key.pem 文件放到同目录，编译时 embed。
//
// 接入只需要 3 步：
//   1. 拷贝本文件 + master_public_key.pem 到 backend/internal/license/
//   2. 在 backend/cmd/main.go 里启动时调用 license.MustVerify(...)
//   3. config.json 里加 license.token 字段
package license

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

//go:embed master_public_key.pem
var masterPublicKeyPEM []byte

// Payload 是 token 解出来的内容，跟 master 端的定义保持一致。
type Payload struct {
	LicenseID    string    `json:"license_id"`
	CustomerID   uint      `json:"customer_id"`
	CustomerName string    `json:"customer_name"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Version      int       `json:"version"`
}

// Verify 离线校验 token：验签名 + 验过期时间。
//
// 完全离线，不需要联网，不依赖 master。
func Verify(token string) (*Payload, error) {
	pub, err := loadPublicKey()
	if err != nil {
		return nil, fmt.Errorf("内嵌公钥加载失败: %w", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, errors.New("license token 格式错误")
	}
	bodyB64, sigB64 := parts[0], parts[1]

	body, err := base64.RawURLEncoding.DecodeString(bodyB64)
	if err != nil {
		return nil, errors.New("license token 格式错误")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, errors.New("license token 格式错误")
	}

	hashed := sha256.Sum256([]byte(bodyB64))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], sig); err != nil {
		return nil, errors.New("license 签名无效（不是合法的 master 签发）")
	}

	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, errors.New("license payload 解析失败")
	}
	if p.Version != 1 {
		return nil, fmt.Errorf("不支持的 license 协议版本: %d", p.Version)
	}
	if time.Now().After(p.ExpiresAt) {
		return nil, fmt.Errorf("license 已过期 (expired_at=%s)", p.ExpiresAt.Format("2006-01-02"))
	}
	return &p, nil
}

// MustVerify 校验失败直接 panic / log.Fatal。
// 在 main.go 启动时调用，让没有合法 license 的客户启动不起来。
func MustVerify(token string) *Payload {
	p, err := Verify(token)
	if err != nil {
		panic(fmt.Sprintf("license 校验失败: %v", err))
	}
	return p
}

func loadPublicKey() (*rsa.PublicKey, error) {
	block, _ := pem.Decode(masterPublicKeyPEM)
	if block == nil {
		return nil, errors.New("master_public_key.pem 解析失败")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("公钥不是 RSA 类型")
	}
	return rsaPub, nil
}
