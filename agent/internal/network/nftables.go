package network

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

type Nftables struct {
	WANInterface string
	NATCIDR      string
	Runner       CommandRunner
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, stdin string) ([]byte, error)
}

type SystemRunner struct{}

func (SystemRunner) Run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	return cmd.CombinedOutput()
}

func NewNftables(wanInterface, natCIDR string) *Nftables {
	if wanInterface == "" {
		wanInterface = "eth0"
	}
	if natCIDR == "" {
		natCIDR = "10.0.0.0/24"
	}

	return &Nftables{WANInterface: wanInterface, NATCIDR: natCIDR, Runner: SystemRunner{}}
}

func (n *Nftables) Name() string {
	return "nftables"
}

func (n *Nftables) VerifyNAT(ctx context.Context, externalIP string, rules []NATRule) (Verification, error) {
	expected := natKeys(externalIP, rules)
	actual := []string{}

	if n.Runner == nil {
		n.Runner = SystemRunner{}
	}
	output, _ := n.Runner.Run(ctx, "nft", []string{"list", "table", "inet", "deviy_nat"}, "")

	pattern := regexp.MustCompile(`ip daddr (\S+) (tcp|udp) dport (\d+) dnat to (\S+):(\d+)`)
	for _, match := range pattern.FindAllStringSubmatch(string(output), -1) {
		actual = append(actual, fmt.Sprintf("%s:%s/%s->%s:%s", match[1], match[3], match[2], match[4], match[5]))
	}

	sort.Strings(actual)
	sort.Strings(expected)
	missing, extra := diffKeys(expected, actual)

	return Verification{
		Backend:  "nftables",
		Verified: len(missing) == 0 && len(extra) == 0,
		Expected: expected,
		Actual:   actual,
		Missing:  missing,
		Extra:    extra,
	}, nil
}

func (n *Nftables) PlanNAT(externalIP string, rules []NATRule) (Plan, error) {
	if err := ValidateNAT(externalIP, rules); err != nil {
		return Plan{}, err
	}

	var script strings.Builder
	script.WriteString("table inet deviy_nat {\n")
	script.WriteString("  chain prerouting {\n")
	script.WriteString("    type nat hook prerouting priority -100; policy accept;\n")
	for _, rule := range rules {
		script.WriteString(fmt.Sprintf(
			"    ip daddr %s %s dport %d dnat to %s:%d\n",
			externalIP,
			strings.ToLower(rule.Protocol),
			rule.ExternalPort,
			rule.InternalIP,
			rule.InternalPort,
		))
	}
	script.WriteString("  }\n")
	script.WriteString("  chain postrouting {\n")
	script.WriteString("    type nat hook postrouting priority 100; policy accept;\n")
	script.WriteString(fmt.Sprintf(
		"    ip saddr %s oifname \"%s\" masquerade\n",
		n.NATCIDR,
		n.WANInterface,
	))
	script.WriteString("  }\n")
	script.WriteString("}\n")
	script.WriteString("table inet deviy_forward {\n")
	script.WriteString("  chain forward {\n")
	script.WriteString("    type filter hook forward priority -10; policy accept;\n")
	script.WriteString("    ct status dnat accept\n")
	script.WriteString("  }\n")
	script.WriteString("}\n")

	return Plan{
		Backend:   "nftables",
		Kind:      "nat",
		Summary:   fmt.Sprintf("%s 上 %d 条 NAT 映射", externalIP, len(rules)),
		Diff:      natDiff(externalIP, rules),
		Script:    script.String(),
		Applyable: true,
	}, nil
}

func (n *Nftables) PlanFirewall(rules []FirewallRule) (Plan, error) {
	if err := ValidateFirewall(rules); err != nil {
		return Plan{}, err
	}

	var script strings.Builder
	script.WriteString("table inet deviy_filter {\n")
	script.WriteString("  chain input {\n")
	script.WriteString("    type filter hook input priority 0; policy accept;\n")
	script.WriteString("    ct state established,related accept\n")
	script.WriteString("    iif lo accept\n")
	for _, rule := range rules {
		protocol := rule.Protocol
		if protocol == "" {
			protocol = "all"
		}
		action := "accept"
		if rule.Action == "drop" {
			action = "drop"
		}
		source := rule.Source
		if source == "" {
			source = "0.0.0.0/0"
		}
		expression := fmt.Sprintf("    ip saddr %s", source)
		if protocol != "all" {
			expression += " " + protocol
		}
		if rule.Port != "" && protocol != "all" {
			expression += " dport { " + rule.Port + " }"
		}
		expression += " " + action + "\n"
		script.WriteString(expression)
	}
	script.WriteString("  }\n")
	script.WriteString("}\n")

	return Plan{
		Backend:   "nftables",
		Kind:      "firewall",
		Summary:   fmt.Sprintf("%d 条防火墙规则", len(rules)),
		Diff:      firewallDiff(rules),
		Script:    script.String(),
		Applyable: true,
	}, nil
}

func (n *Nftables) Apply(ctx context.Context, plan Plan) error {
	if n.Runner == nil {
		n.Runner = SystemRunner{}
	}

	table := "deviy_nat"
	if plan.Kind == "firewall" {
		table = "deviy_filter"
	}

	_, _ = n.Runner.Run(ctx, "nft", []string{"delete", "table", "inet", table}, "")
	if plan.Kind == "nat" {
		_, _ = n.Runner.Run(ctx, "nft", []string{"delete", "table", "inet", "deviy_forward"}, "")
	}
	output, err := n.Runner.Run(ctx, "nft", []string{"-f", "-"}, plan.Script)
	if err != nil {
		return fmt.Errorf("nftables 应用失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if plan.Kind == "nat" {
		if err := n.applyIPTablesNAT(ctx, plan); err != nil {
			return err
		}
	}

	return nil
}

func (n *Nftables) applyIPTablesNAT(ctx context.Context, plan Plan) error {
	chain := "DEVIY_NAT"
	_, _ = n.Runner.Run(ctx, "iptables", []string{"-t", "nat", "-N", chain}, "")
	_, _ = n.Runner.Run(ctx, "iptables", []string{"-t", "nat", "-F", chain}, "")
	_, _ = n.Runner.Run(ctx, "iptables", []string{"-t", "nat", "-D", "PREROUTING", "-j", chain}, "")
	if output, err := n.Runner.Run(ctx, "iptables", []string{"-t", "nat", "-I", "PREROUTING", "1", "-j", chain}, ""); err != nil {
		return fmt.Errorf("iptables NAT chain 失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	rulePattern := regexp.MustCompile(`(?m)^\s*ip daddr (\S+) (tcp|udp) dport (\d+) dnat to (\S+):(\d+)\s*$`)
	for _, match := range rulePattern.FindAllStringSubmatch(plan.Script, -1) {
		args := []string{
			"-t", "nat", "-A", chain,
			"-d", match[1], "-p", match[2], "--dport", match[3],
			"-j", "DNAT", "--to-destination", match[4] + ":" + match[5],
		}
		if output, err := n.Runner.Run(ctx, "iptables", args, ""); err != nil {
			return fmt.Errorf("iptables DNAT 失败: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	_, _ = n.Runner.Run(ctx, "iptables", []string{"-D", "LIBVIRT_FWI", "-o", "virbr0", "-d", n.NATCIDR, "-j", "ACCEPT"}, "")
	_, _ = n.Runner.Run(ctx, "iptables", []string{"-I", "LIBVIRT_FWI", "1", "-o", "virbr0", "-d", n.NATCIDR, "-j", "ACCEPT"}, "")

	return nil
}

func natDiff(externalIP string, rules []NATRule) string {
	var builder strings.Builder
	builder.WriteString("+ table inet deviy_nat\n")
	for _, rule := range rules {
		builder.WriteString(fmt.Sprintf("+ %s:%d/%s -> %s:%d\n", externalIP, rule.ExternalPort, rule.Protocol, rule.InternalIP, rule.InternalPort))
	}
	builder.WriteString("+ postrouting masquerade\n")

	return builder.String()
}

func firewallDiff(rules []FirewallRule) string {
	var builder strings.Builder
	builder.WriteString("+ table inet deviy_filter\n")
	for _, rule := range rules {
		builder.WriteString(fmt.Sprintf("+ %s %s %s %s %s\n", rule.Direction, rule.Action, rule.Protocol, rule.Port, rule.Source))
	}

	return builder.String()
}
