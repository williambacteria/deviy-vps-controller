package network

import (
	"context"
	"testing"
)

func TestWindowsVerifyNATMatchesWinNAT(t *testing.T) {
	engine := NewWindows()
	engine.Runner = &verifyPowerShell{output: `[{"Protocol":"Tcp","ExternalIPAddress":"0.0.0.0","ExternalPort":21000,"InternalIPAddress":"10.0.0.12","InternalPort":22}]`}

	result, err := engine.VerifyNAT(context.Background(), "203.0.113.88", []NATRule{
		{ExternalPort: 21000, InternalIP: "10.0.0.12", InternalPort: 22, Protocol: "tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified {
		t.Fatalf("expected verified, missing=%v extra=%v", result.Missing, result.Extra)
	}
}

type verifyPowerShell struct {
	output string
}

func (v *verifyPowerShell) Run(_ context.Context, _ string) ([]byte, error) {
	return []byte(v.output), nil
}
