package controller

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func LoadServerTLS(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("Controller TLS 证书和私钥必须同时配置")
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("加载 Controller TLS 证书失败: %w", err)
	}

	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
	if clientCAFile != "" {
		pool, err := loadCA(clientCAFile)
		if err != nil {
			return nil, err
		}
		config.ClientCAs = pool
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return config, nil
}

func LoadClientTLS(caFile, certFile, keyFile string) (*tls.Config, error) {
	if caFile == "" && certFile == "" && keyFile == "" {
		return nil, nil
	}

	config := &tls.Config{MinVersion: tls.VersionTLS13}
	if caFile != "" {
		pool, err := loadCA(caFile)
		if err != nil {
			return nil, err
		}
		config.RootCAs = pool
	}
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, fmt.Errorf("Controller 客户端证书和私钥必须同时配置")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("加载 Controller 客户端证书失败: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}
	}

	return config, nil
}

func loadCA(path string) (*x509.CertPool, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 Controller CA 失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(payload) {
		return nil, fmt.Errorf("Controller CA 文件不合法")
	}

	return pool, nil
}
