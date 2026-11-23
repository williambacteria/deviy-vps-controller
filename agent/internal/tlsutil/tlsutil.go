package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func ServerConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("TLS 证书和私钥必须同时配置")
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("加载 TLS 证书失败: %w", err)
	}

	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	if clientCAFile != "" {
		pool, err := loadCAPool(clientCAFile)
		if err != nil {
			return nil, err
		}
		config.ClientCAs = pool
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return config, nil
}

func ClientConfig(caFile, certFile, keyFile string) (*tls.Config, error) {
	if caFile == "" && certFile == "" && keyFile == "" {
		return nil, nil
	}

	config := &tls.Config{MinVersion: tls.VersionTLS13}

	if caFile != "" {
		pool, err := loadCAPool(caFile)
		if err != nil {
			return nil, err
		}
		config.RootCAs = pool
	}

	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, fmt.Errorf("客户端证书和私钥必须同时配置")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("加载客户端证书失败: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}
	}

	return config, nil
}

func loadCAPool(path string) (*x509.CertPool, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 CA 失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(payload) {
		return nil, fmt.Errorf("CA 文件不包含有效 PEM 证书")
	}

	return pool, nil
}
