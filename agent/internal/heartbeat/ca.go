package heartbeat

import (
	"crypto/x509"
	"fmt"
	"os"
)

func loadCA(path string) (*x509.CertPool, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取心跳 CA 失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(payload) {
		return nil, fmt.Errorf("心跳 CA 文件不合法")
	}

	return pool, nil
}
