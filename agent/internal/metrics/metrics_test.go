package metrics

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseProcNetDev(t *testing.T) {
	payload := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0
 vmbr0: 123456789 100 0 0 0 0 0 0 987654321 200 0 0 0 0 0 0
`

	counter, err := ParseProcNetDev(payload, "vmbr0")
	if err != nil {
		t.Fatal(err)
	}
	if counter.Interface != "vmbr0" || counter.RxBytes != 123456789 || counter.TxBytes != 987654321 {
		t.Fatalf("unexpected counter: %+v", counter)
	}

	fallback, err := ParseProcNetDev(payload, "eth9")
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Interface != "vmbr0" {
		t.Fatalf("expected fallback to non-loopback interface, got %s", fallback.Interface)
	}
}

func TestParseWindowsCounters(t *testing.T) {
	counter, err := ParseWindowsCounters([]byte(`{"Name":"Ethernet","ReceivedBytes":123456,"SentBytes":654321}`))
	if err != nil {
		t.Fatal(err)
	}
	if counter.Interface != "Ethernet" || counter.RxBytes != 123456 || counter.TxBytes != 654321 {
		t.Fatalf("unexpected counter: %+v", counter)
	}
}

type sequenceCollector struct {
	step uint64
}

func (s *sequenceCollector) Read(_ context.Context) (Counters, error) {
	s.step++

	return Counters{
		Interface: "vmbr0",
		RxBytes:   1_000_000 + s.step*2_000_000,
		TxBytes:   500_000 + s.step*500_000,
	}, nil
}

func TestMonitorComputesRatesAndHistory(t *testing.T) {
	monitor := NewMonitor(&sequenceCollector{}, time.Second)
	time.Sleep(15 * time.Millisecond)
	snapshot := monitor.Sample(context.Background())

	if len(snapshot.Points) < 2 {
		t.Fatalf("expected history points, got %d", len(snapshot.Points))
	}
	if snapshot.RxMbps <= 0 || snapshot.TxMbps <= 0 {
		t.Fatalf("expected positive rates, got rx=%f tx=%f", snapshot.RxMbps, snapshot.TxMbps)
	}
	if !strings.HasSuffix(snapshot.TotalRx, "MB") || !strings.HasSuffix(snapshot.TotalTx, "MB") {
		t.Fatalf("unexpected totals: %s / %s", snapshot.TotalRx, snapshot.TotalTx)
	}
	if snapshot.Interface != "vmbr0" {
		t.Fatalf("unexpected interface: %s", snapshot.Interface)
	}
}
