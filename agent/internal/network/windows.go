package network

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

type Mock struct{}

func NewMock() *Mock {
	return &Mock{}
}

func (m *Mock) Name() string {
	return "mock"
}

func (m *Mock) PlanNAT(externalIP string, rules []NATRule) (Plan, error) {
	if err := ValidateNAT(externalIP, rules); err != nil {
		return Plan{}, err
	}

	return Plan{
		Backend:   "mock",
		Kind:      "nat",
		Summary:   fmt.Sprintf("mock: %d NAT rules", len(rules)),
		Diff:      natDiff(externalIP, rules),
		Script:    "# mock backend: NAT rules accepted in memory\n",
		Applyable: true,
	}, nil
}

func (m *Mock) PlanFirewall(rules []FirewallRule) (Plan, error) {
	if err := ValidateFirewall(rules); err != nil {
		return Plan{}, err
	}

	return Plan{
		Backend:   "mock",
		Kind:      "firewall",
		Summary:   fmt.Sprintf("mock: %d firewall rules", len(rules)),
		Diff:      firewallDiff(rules),
		Script:    "# mock backend: firewall rules accepted in memory\n",
		Applyable: true,
	}, nil
}

func (m *Mock) Apply(_ context.Context, _ Plan) error {
	return nil
}

func (m *Mock) VerifyNAT(_ context.Context, externalIP string, rules []NATRule) (Verification, error) {
	keys := natKeys(externalIP, rules)
	sort.Strings(keys)

	return Verification{
		Backend:  "mock",
		Verified: true,
		Expected: keys,
		Actual:   keys,
	}, nil
}

type Windows struct {
	SwitchName string
	NatName    string
	NATCIDR    string
	Runner     PowerShellRunner
}

type PowerShellRunner interface {
	Run(ctx context.Context, script string) ([]byte, error)
}

func NewWindows() *Windows {
	return &Windows{SwitchName: "Default Switch", NatName: "DeviyNAT", NATCIDR: "10.0.0.0/24", Runner: SystemPowerShell{}}
}

func NewWindowsWithConfig(natName, natCIDR string) *Windows {
	windows := NewWindows()
	if natName != "" {
		windows.NatName = natName
	}
	if natCIDR != "" {
		windows.NATCIDR = natCIDR
	}

	return windows
}

type SystemPowerShell struct{}

func (SystemPowerShell) Run(ctx context.Context, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)

	return cmd.CombinedOutput()
}

func (w *Windows) Name() string {
	return "windows"
}

func (w *Windows) VerifyNAT(ctx context.Context, externalIP string, rules []NATRule) (Verification, error) {
	expected := natKeys(externalIP, rules)
	actual := []string{}
	if w.Runner == nil {
		w.Runner = SystemPowerShell{}
	}

	script := fmt.Sprintf(
		"$natName = '%s'\n@(Get-NetNatStaticMapping -NatName $natName -ErrorAction SilentlyContinue | Select-Object Protocol,ExternalIPAddress,ExternalPort,InternalIPAddress,InternalPort) | ConvertTo-Json -Compress\n",
		strings.ReplaceAll(w.NatName, "'", "''"),
	)
	output, err := w.Runner.Run(ctx, script)
	if err != nil {
		return Verification{}, fmt.Errorf("read WinNAT mappings: %w", err)
	}

	var mappings []struct {
		Protocol     string `json:"Protocol"`
		ExternalPort int    `json:"ExternalPort"`
		InternalIP   string `json:"InternalIPAddress"`
		InternalPort int    `json:"InternalPort"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &mappings); err != nil {
		return Verification{}, fmt.Errorf("parse WinNAT mappings: %w", err)
	}
	for _, mapping := range mappings {
		actual = append(actual, fmt.Sprintf(
			"%s:%d/%s->%s:%d",
			externalIP, mapping.ExternalPort, strings.ToLower(mapping.Protocol), mapping.InternalIP, mapping.InternalPort,
		))
	}

	sort.Strings(actual)
	sort.Strings(expected)
	missing, extra := diffKeys(expected, actual)

	return Verification{
		Backend:  "windows-winnat",
		Verified: len(missing) == 0 && len(extra) == 0,
		Expected: expected,
		Actual:   actual,
		Missing:  missing,
		Extra:    extra,
	}, nil
}

func (w *Windows) PlanNAT(externalIP string, rules []NATRule) (Plan, error) {
	if err := ValidateNAT(externalIP, rules); err != nil {
		return Plan{}, err
	}

	natName := strings.ReplaceAll(w.NatName, "'", "''")
	var script strings.Builder
	script.WriteString("$ErrorActionPreference = 'Stop'\n")
	script.WriteString(fmt.Sprintf("$natName = '%s'\n", natName))
	script.WriteString("if (-not (Get-NetNat -Name $natName -ErrorAction SilentlyContinue)) { throw \"WinNAT instance not found: $natName\" }\n")
	script.WriteString("Get-NetNatStaticMapping -NatName $natName -ErrorAction SilentlyContinue | Remove-NetNatStaticMapping\n")
	script.WriteString("Get-NetFirewallRule -DisplayName 'Deviy-NAT-*' -ErrorAction SilentlyContinue | Remove-NetFirewallRule\n")
	for _, rule := range rules {
		protocol := strings.ToUpper(rule.Protocol)
		if protocol == "" {
			protocol = "TCP"
		}
		script.WriteString(fmt.Sprintf(
			"Add-NetNatStaticMapping -NatName $natName -Protocol %s -ExternalIPAddress '0.0.0.0' -ExternalPort %d -InternalIPAddress '%s' -InternalPort %d | Out-Null\n",
			protocol, rule.ExternalPort, rule.InternalIP, rule.InternalPort,
		))
		script.WriteString(fmt.Sprintf(
			"New-NetFirewallRule -DisplayName 'Deviy-NAT-%d' -Direction Inbound -Action Allow -Protocol %s -LocalPort %d | Out-Null\n",
			rule.ExternalPort, protocol, rule.ExternalPort,
		))
	}

	return Plan{
		Backend:   "windows-winnat",
		Kind:      "nat",
		Summary:   fmt.Sprintf("WinNAT %s: %d static mappings", w.NatName, len(rules)),
		Diff:      natDiff(externalIP, rules),
		Script:    script.String(),
		Applyable: true,
	}, nil
}

func (w *Windows) PlanFirewall(rules []FirewallRule) (Plan, error) {
	if err := ValidateFirewall(rules); err != nil {
		return Plan{}, err
	}

	var script strings.Builder
	script.WriteString("$ErrorActionPreference = 'Stop'\n")
	script.WriteString("Get-NetFirewallRule -DisplayName 'Deviy-FW-*' -ErrorAction SilentlyContinue | Remove-NetFirewallRule\n")
	for i, rule := range rules {
		action := "Allow"
		if rule.Action == "drop" {
			action = "Block"
		}
		protocol := strings.ToUpper(rule.Protocol)
		if protocol == "" || protocol == "ALL" {
			protocol = "Any"
		}
		script.WriteString(fmt.Sprintf(
			"New-NetFirewallRule -DisplayName 'Deviy-FW-%d' -Direction %s -Action %s -Protocol %s",
			i+1, directionForWindows(rule.Direction), action, protocol,
		))
		if rule.Port != "" && protocol != "Any" {
			script.WriteString(fmt.Sprintf(" -LocalPort %s", rule.Port))
		}
		if rule.Source != "" {
			script.WriteString(fmt.Sprintf(" -RemoteAddress %s", rule.Source))
		}
		script.WriteString(" | Out-Null\n")
	}

	return Plan{
		Backend:   "windows-winnat",
		Kind:      "firewall",
		Summary:   fmt.Sprintf("Windows Firewall: %d rules", len(rules)),
		Diff:      firewallDiff(rules),
		Script:    script.String(),
		Applyable: true,
	}, nil
}

func (w *Windows) Apply(ctx context.Context, plan Plan) error {
	if w.Runner == nil {
		return fmt.Errorf("Windows network runner is not configured")
	}

	output, err := w.Runner.Run(ctx, plan.Script)
	if err != nil {
		return fmt.Errorf("apply Windows network failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func directionForWindows(direction string) string {
	if direction == "out" {
		return "Outbound"
	}

	return "Inbound"
}
