package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

type NATRule struct {
	ID           string `json:"id"`
	ExternalIP   string `json:"external_ip"`
	ExternalPort int    `json:"external_port"`
	InternalIP   string `json:"internal_ip"`
	InternalPort int    `json:"internal_port"`
	Protocol     string `json:"protocol"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
}

type FirewallRule struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Action    string `json:"action"`
	Protocol  string `json:"protocol"`
	Port      string `json:"port"`
	Source    string `json:"source"`
	Comment   string `json:"comment"`
}

type Plan struct {
	Backend   string `json:"backend"`
	Kind      string `json:"kind"`
	Summary   string `json:"summary"`
	Diff      string `json:"diff"`
	Script    string `json:"script"`
	Applyable bool   `json:"applyable"`
}

type Verification struct {
	Backend  string   `json:"backend"`
	Verified bool     `json:"verified"`
	Expected []string `json:"expected"`
	Actual   []string `json:"actual"`
	Missing  []string `json:"missing"`
	Extra    []string `json:"extra"`
}

type NATVerifier interface {
	VerifyNAT(ctx context.Context, externalIP string, rules []NATRule) (Verification, error)
}

type Engine interface {
	Name() string
	PlanNAT(externalIP string, rules []NATRule) (Plan, error)
	PlanFirewall(rules []FirewallRule) (Plan, error)
	Apply(ctx context.Context, plan Plan) error
}

func ValidateNAT(externalIP string, rules []NATRule) error {
	if net.ParseIP(externalIP) == nil {
		return fmt.Errorf("外部 IP 不合法: %s", externalIP)
	}

	seen := map[string]bool{}
	for _, rule := range rules {
		if net.ParseIP(rule.InternalIP) == nil {
			return fmt.Errorf("内部 IP 不合法: %s", rule.InternalIP)
		}
		if rule.ExternalPort < 1 || rule.ExternalPort > 65535 || rule.InternalPort < 1 || rule.InternalPort > 65535 {
			return errors.New("端口必须在 1–65535 之间")
		}
		protocol := strings.ToLower(rule.Protocol)
		if protocol != "tcp" && protocol != "udp" {
			return fmt.Errorf("不支持的协议: %s", rule.Protocol)
		}
		key := fmt.Sprintf("%s/%d/%s", externalIP, rule.ExternalPort, protocol)
		if seen[key] {
			return fmt.Errorf("端口冲突: %s:%d/%s", externalIP, rule.ExternalPort, protocol)
		}
		seen[key] = true
	}

	return nil
}

func ValidateFirewall(rules []FirewallRule) error {
	for _, rule := range rules {
		if rule.Direction != "" && rule.Direction != "in" && rule.Direction != "out" {
			return fmt.Errorf("不支持的防火墙方向: %s", rule.Direction)
		}
		if rule.Action != "accept" && rule.Action != "drop" {
			return fmt.Errorf("不支持的防火墙动作: %s", rule.Action)
		}
		if rule.Protocol != "" && rule.Protocol != "tcp" && rule.Protocol != "udp" && rule.Protocol != "all" {
			return fmt.Errorf("不支持的防火墙协议: %s", rule.Protocol)
		}
	}

	return nil
}

func portList(port string) ([]int, error) {
	if strings.TrimSpace(port) == "" {
		return nil, nil
	}

	parts := strings.Split(port, ",")
	ports := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			start, err1 := strconv.Atoi(bounds[0])
			end, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil || start < 1 || end > 65535 || start > end {
				return nil, fmt.Errorf("端口范围不合法: %s", part)
			}
			ports = append(ports, start, end)
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 1 || value > 65535 {
			return nil, fmt.Errorf("端口不合法: %s", part)
		}
		ports = append(ports, value)
	}

	return ports, nil
}

func natKeys(externalIP string, rules []NATRule) []string {
	keys := make([]string, 0, len(rules))
	for _, rule := range rules {
		keys = append(keys, fmt.Sprintf(
			"%s:%d/%s->%s:%d",
			externalIP,
			rule.ExternalPort,
			strings.ToLower(rule.Protocol),
			rule.InternalIP,
			rule.InternalPort,
		))
	}

	return keys
}

func diffKeys(expected, actual []string) (missing, extra []string) {
	expectedSet := map[string]bool{}
	for _, key := range expected {
		expectedSet[key] = true
	}
	actualSet := map[string]bool{}
	for _, key := range actual {
		actualSet[key] = true
	}

	for _, key := range expected {
		if !actualSet[key] {
			missing = append(missing, key)
		}
	}
	for _, key := range actual {
		if !expectedSet[key] {
			extra = append(extra, key)
		}
	}

	return missing, extra
}
