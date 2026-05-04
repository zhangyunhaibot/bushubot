package license

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerify(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)

	p := Payload{
		LicenseID:    "lic_test_001",
		CustomerID:   42,
		CustomerName: "测试客户",
		IssuedAt:     time.Now(),
		ExpiresAt:    time.Now().Add(365 * 24 * time.Hour),
	}
	tok, err := Sign(p, priv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tok, ".") {
		t.Fatal("token 应该是两段")
	}

	got, err := Verify(tok, &priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.LicenseID != p.LicenseID || got.CustomerID != p.CustomerID {
		t.Fatalf("解出来不对: %+v", got)
	}
}

func TestVerifyExpired(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	tok, _ := Sign(Payload{
		ExpiresAt: time.Now().Add(-time.Hour),
	}, priv)

	if _, err := Verify(tok, &priv.PublicKey); err != ErrExpired {
		t.Fatalf("应当返回 ErrExpired, 实际: %v", err)
	}
}

func TestVerifyTampered(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	tok, _ := Sign(Payload{
		ExpiresAt: time.Now().Add(time.Hour),
	}, priv)

	// 篡改 payload 部分
	parts := strings.Split(tok, ".")
	parts[0] = parts[0] + "X"
	tampered := parts[0] + "." + parts[1]

	if _, err := Verify(tampered, &priv.PublicKey); err == nil {
		t.Fatal("篡改后应当验证失败")
	}
}

func TestVerifyWrongKey(t *testing.T) {
	priv1, _ := rsa.GenerateKey(rand.Reader, 2048)
	priv2, _ := rsa.GenerateKey(rand.Reader, 2048)

	tok, _ := Sign(Payload{ExpiresAt: time.Now().Add(time.Hour)}, priv1)
	if _, err := Verify(tok, &priv2.PublicKey); err != ErrBadSignature {
		t.Fatalf("应当返回 ErrBadSignature, 实际: %v", err)
	}
}

func TestVerifyNotYetValid(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	// IssuedAt 设为 1 小时后（已经超出 5 分钟容忍）
	tok, _ := Sign(Payload{
		IssuedAt:  time.Now().Add(time.Hour),
		ExpiresAt: time.Now().Add(2 * time.Hour),
	}, priv)
	if _, err := Verify(tok, &priv.PublicKey); err != ErrNotYetValid {
		t.Fatalf("应当返回 ErrNotYetValid, 实际: %v", err)
	}
}

func TestVerifyClockSkewTolerated(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	// IssuedAt 设为 1 分钟后（在 5 分钟容忍内，应该通过）
	tok, _ := Sign(Payload{
		IssuedAt:  time.Now().Add(time.Minute),
		ExpiresAt: time.Now().Add(time.Hour),
	}, priv)
	if _, err := Verify(tok, &priv.PublicKey); err != nil {
		t.Fatalf("5 分钟内的时钟偏差应当通过, 实际: %v", err)
	}
}
