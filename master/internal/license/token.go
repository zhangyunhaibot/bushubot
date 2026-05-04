// Package license 实现 bushubot 的离线 license token 签发与校验。
//
// Token 格式（参考 JWT，但更简单，不依赖第三方库）:
//   <payload_b64url>.<signature_b64url>
//
// payload 是 JSON，signature 是 RSA-SHA256(payload) 用 master 私钥签名。
// 客户端用内嵌的公钥就能离线验证，**不需要联网**。
package license

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Token Header 信息（不参与签名，仅文档描述）:
//   alg = RS256, typ = BUSHU-LIC
//
// Payload 包含的字段:
type Payload struct {
	LicenseID    string    `json:"license_id"`    // 唯一标识，便于吊销
	CustomerID   uint      `json:"customer_id"`   // master 数据库中的客户 ID
	CustomerName string    `json:"customer_name"` // 客户名（仅展示，不参与权限）
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Version      int       `json:"version"` // 协议版本，便于将来演化
}

// Sign 用私钥签发一个 token。
func Sign(p Payload, priv *rsa.PrivateKey) (string, error) {
	if p.Version == 0 {
		p.Version = 1
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	bodyB64 := base64.RawURLEncoding.EncodeToString(body)

	hashed := sha256.Sum256([]byte(bodyB64))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed[:])
	if err != nil {
		return "", err
	}
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	return bodyB64 + "." + sigB64, nil
}

// Verify 用公钥验证 token 签名 + 过期时间。
//
// 错误情况：
//   - ErrMalformed: 格式错误
//   - ErrBadSignature: 签名无效（被篡改 / 不是你的私钥签的）
//   - ErrExpired: 已过期
func Verify(token string, pub *rsa.PublicKey) (*Payload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrMalformed
	}
	bodyB64, sigB64 := parts[0], parts[1]

	body, err := base64.RawURLEncoding.DecodeString(bodyB64)
	if err != nil {
		return nil, ErrMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, ErrMalformed
	}

	hashed := sha256.Sum256([]byte(bodyB64))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], sig); err != nil {
		return nil, ErrBadSignature
	}

	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, ErrMalformed
	}
	if p.Version != 1 {
		return nil, fmt.Errorf("不支持的 license 协议版本: %d", p.Version)
	}
	// 允许 5 分钟时钟偏差（避免 master/agent 时钟微差导致误拒）
	if time.Now().Add(5 * time.Minute).Before(p.IssuedAt) {
		return nil, ErrNotYetValid
	}
	if time.Now().After(p.ExpiresAt) {
		return nil, ErrExpired
	}
	return &p, nil
}

var (
	ErrMalformed    = errors.New("license token 格式错误")
	ErrBadSignature = errors.New("license token 签名无效")
	ErrExpired      = errors.New("license token 已过期")
	ErrNotYetValid  = errors.New("license token 尚未生效")
)
