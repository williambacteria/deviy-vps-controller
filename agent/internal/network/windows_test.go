package network

import (
	"context"
	"strings"
	"testing"
)

type fakePowerShell struct {
	scripts []string
}

func (f *fakePowerShell) Run(_ context.Context, script string) ([]byte, error) {
	f.scripts = append(f.scripts, script)

	return []byte{}, nil
}

func TestWindowsPlanNAT(t *testing.T) {
	runner := &fakePowerShell{}
	engine := NewWindows()
	engine.Runner = runner

	plan, err := engine.PlanNAT("203.0.113.88", []NATRule{
		{ExternalPort: 20000, InternalIP: "10.0.0.12", InternalPort: 22, Protocol: "tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Script, "Add-NetNatStaticMapping") || !strings.Contains(plan.Script, "New-NetFirewallRule") {
		t.Fatalf("unexpected script: %s", plan.Script)
	}
	if !strings.Contains(plan.Script, "-ExternalIPAddress '0.0.0.0'") || !strings.Contains(plan.Script, "Get-NetNatStaticMapping") {
		t.Fatalf("expected WinNAT static mapping behavior: %s", plan.Script)
	}
	if err := engine.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if len(runner.scripts) != 1 {
		t.Fatalf("expected one powershell call, got %d", len(runner.scripts))
	}
}

func TestWindowsPlanFirewall(t *testing.T) {
	engine := NewWindows()
	plan, err := engine.PlanFirewall([]FirewallRule{
		{Direction: "in", Action: "accept", Protocol: "tcp", Port: "22", Source: "0.0.0.0/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Script, "-Direction Inbound") || !strings.Contains(plan.Script, "-LocalPort 22") {
		t.Fatalf("unexpected script: %s", plan.Script)
	}
}
