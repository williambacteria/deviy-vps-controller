package heartbeat

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	ControllerURL string
	CAFile        string
	CertFile      string
	KeyFile       string
	NodeID        string
	Name          string
	Provider      string
	Region        string
	PublicURL     string
	Version       string
	RegisterToken string
	Interval      time.Duration
}

type Client struct {
	cfg    Config
	client *http.Client
}

func New(cfg Config) (*Client, error) {
	if cfg.ControllerURL == "" {
		return nil, nil
	}
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("heartbeat node id is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Second
	}

	client := &http.Client{Timeout: 10 * time.Second}
	if cfg.CAFile != "" || cfg.CertFile != "" || cfg.KeyFile != "" {
		if cfg.CAFile == "" || cfg.CertFile == "" || cfg.KeyFile == "" {
			return nil, fmt.Errorf("heartbeat mTLS 需要 CA、客户端证书和私钥")
		}
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, err
		}
		pool, err := loadCA(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			RootCAs:      pool,
			Certificates: []tls.Certificate{cert},
		}}
	}

	return &Client{cfg: cfg, client: client}, nil
}

func (c *Client) Run(ctx context.Context, snapshot func() map[string]any) {
	c.register(ctx)

	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	c.heartbeat(ctx, snapshot)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.heartbeat(ctx, snapshot)
		}
	}
}

func (c *Client) register(ctx context.Context) {
	payload := map[string]any{
		"node_id":  c.cfg.NodeID,
		"name":     c.cfg.Name,
		"provider": c.cfg.Provider,
		"region":   c.cfg.Region,
		"url":      c.cfg.PublicURL,
		"version":  c.cfg.Version,
	}
	if err := c.post(ctx, "/api/agent/register", payload); err != nil {
		log.Printf("agent register failed: %v", err)
	}
}

func (c *Client) heartbeat(ctx context.Context, snapshot func() map[string]any) {
	payload := snapshot()
	if payload == nil {
		payload = map[string]any{}
	}
	payload["node_id"] = c.cfg.NodeID
	if c.cfg.Version != "" {
		payload["version"] = c.cfg.Version
	}

	if err := c.post(ctx, "/api/agent/heartbeat", payload); err != nil {
		log.Printf("agent heartbeat failed: %v", err)
	}
}

func (c *Client) post(ctx context.Context, path string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(c.cfg.ControllerURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.RegisterToken != "" {
		req.Header.Set("X-Deviy-Register-Token", c.cfg.RegisterToken)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d", path, resp.StatusCode)
	}

	return nil
}
