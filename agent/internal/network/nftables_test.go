package network

import (
	"context"
	"strings"
	"testing"
)

type fakeCommand struct {
	names  []string
	args   [][]string
	stdins []string
}

func (f *fakeCommand) Run(_ context.Context, name string, args []string, stdin string) ([]byte, error) {
	f.names = append(f.names, name)
	f.args = append(f.args, args)
	f.stdins = append(f.stdins, stdin)

	return []byte{}, nil
}

func TestNftablesPlanAndApply(t *testing.T) {
	runner := &fakeCommand{}
	engine := NewNftables("eth0", "10.0.0.0/24")
	engine.Runner = runner

	plan, err := engine.PlanNAT("203.0.113.88", []NATRule{
		{ExternalPort: 20000, InternalIP: "10.0.0.12", InternalPort: 22, Protocol: "tcp"},
		{ExternalPort: 20001, InternalIP: "10.0.0.12", InternalPort: 80, Protocol: "tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Script, "dnat to 10.0.0.12:22") || !strings.Contains(plan.Script, "masquerade") {
		t.Fatalf("unexpected script: %s", plan.Script)
	}
	if !strings.Contains(plan.Diff, "203.0.113.88:20000/tcp") {
		t.Fatalf("unexpected diff: %s", plan.Diff)
	}
	if err := engine.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if len(runner.names) < 2 || runner.names[0] != "nft" || runner.args[0][0] != "delete" {
		t.Fatalf("unexpected apply commands: %+v", runner.names)
	}
	if !strings.Contains(runner.stdins[2], "deviy_nat") {
		t.Fatalf("missing script on stdin: %s", runner.stdins[2])
	}
}

func TestNftablesFirewallPlan(t *testing.T) {
	engine := NewNftables("eth0", "10.0.0.0/24")
	plan, err := engine.PlanFirewall([]FirewallRule{
		{Direction: "in", Action: "accept", Protocol: "tcp", Port: "22", Source: "0.0.0.0/0"},
		{Direction: "in", Action: "drop", Protocol: "all", Source: "0.0.0.0/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Script, "deviy_filter") || !strings.Contains(plan.Script, "tcp dport { 22 }") {
		t.Fatalf("unexpected script: %s", plan.Script)
	}
}

func TestNATValidation(t *testing.T) {
	engine := NewNftables("", "")

	if _, err := engine.PlanNAT("not-an-ip", nil); err == nil {
		t.Fatal("expected invalid ip error")
	}
	if _, err := engine.PlanNAT("203.0.113.8", []NATRule{{ExternalPort: 0, InternalIP: "10.0.0.2", InternalPort: 22, Protocol: "tcp"}}); err == nil {
		t.Fatal("expected port error")
	}
	if _, err := engine.PlanNAT("203.0.113.8", []NATRule{
		{ExternalPort: 20000, InternalIP: "10.0.0.2", InternalPort: 22, Protocol: "tcp"},
		{ExternalPort: 20000, InternalIP: "10.0.0.3", InternalPort: 22, Protocol: "tcp"},
	}); err == nil {
		t.Fatal("expected duplicate port error")
	}
}
