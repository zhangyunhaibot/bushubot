package license

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// LoadOrGenerateKeypair 从指定目录加载 RSA 密钥对；不存在则生成 4096 位密钥。
//
// 文件路径：
//   <dir>/master.key  (私钥, PEM PKCS#1, 权限 600)
//   <dir>/master.pub  (公钥, PEM PKIX,   权限 644)
//
// 私钥**必须**留在 master 服务器，永不外泄。公钥要拷贝到 TGfulibot 项目内嵌。
func LoadOrGenerateKeypair(dir string) (*rsa.PrivateKey, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	privPath := filepath.Join(dir, "master.key")
	pubPath := filepath.Join(dir, "master.pub")

	if _, err := os.Stat(privPath); err == nil {
		return loadPrivateKey(privPath)
	}

	priv, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, fmt.Errorf("生成 RSA 密钥失败: %w", err)
	}
	if err := writePrivateKey(privPath, priv); err != nil {
		return nil, err
	}
	if err := writePublicKey(pubPath, &priv.PublicKey); err != nil {
		return nil, err
	}
	return priv, nil
}

// LoadPublicKey 从 PEM 文件读公钥。提供给 TGfulibot 集成代码复用。
func LoadPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePublicKeyPEM(data)
}

// ParsePublicKeyPEM 从 PEM 字节解出公钥。
func ParsePublicKeyPEM(data []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("公钥 PEM 解析失败")
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

// PublicKeyPEM 把公钥导出为 PEM 字符串（用于打印给用户拷贝到 TGfulibot 项目）。
func PublicKeyPEM(pub *rsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	if info, err := os.Stat(path); err == nil {
		mode := info.Mode().Perm()
		if mode&0o077 != 0 {
			// 私钥不应该让其他用户能读，警告但不阻止启动（可能有合理原因）
			log.Printf("⚠️ 警告: 私钥文件 %s 权限过宽 (%o)，建议 chmod 600", path, mode)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("私钥 PEM 解析失败")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func writePrivateKey(path string, priv *rsa.PrivateKey) error {
	data := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writePublicKey(path string, pub *rsa.PublicKey) error {
	data, err := PublicKeyPEM(pub)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
