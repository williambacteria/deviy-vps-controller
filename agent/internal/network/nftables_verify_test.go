package network

import (
	"context"
	"strings"
	"testing"
)

type textRunner struct {
	output string
}

func (t textRunner) Run(_ context.Context, _ string, _ []string, _ string) ([]byte, error) {
	return []byte(t.output), nil
}

func TestNftablesVerifyNATDetectsDrift(t *testing.T) {
	engine := NewNftables("eth0", "10.0.0.0/24")
	engine.Runner = textRunner{output: `
table inet deviy_nat {
  chain prerouting {
    ip daddr 203.0.113.88 tcp dport 21000 dnat to 10.0.0.12:22
    ip daddr 203.0.113.88 tcp dport 20000 dnat to 10.0.0.12:80
  }
}
`}

	result, err := engine.VerifyNAT(context.Background(), "203.0.113.88", []NATRule{
		{ExternalPort: 21000, InternalIP: "10.0.0.12", InternalPort: 22, Protocol: "tcp"},
		{ExternalPort: 21001, InternalIP: "10.0.0.12", InternalPort: 443, Protocol: "tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verified {
		t.Fatal("expected drift")
	}
	if len(result.Missing) != 1 || !strings.Contains(result.Missing[0], "21001") {
		t.Fatalf("unexpected missing: %+v", result.Missing)
	}
	if len(result.Extra) != 1 || !strings.Contains(result.Extra[0], "20000") {
		t.Fatalf("unexpected extra: %+v", result.Extra)
	}
}
