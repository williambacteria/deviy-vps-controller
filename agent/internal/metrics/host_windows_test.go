//go:build windows

package metrics

import (
	"context"
	"testing"
)

type fakePowerRunner struct {
	output []byte
}

func (f fakePowerRunner) Run(_ context.Context, _ string) ([]byte, error) {
	return f.output, nil
}

func TestWindowsHostParsesCIMetrics(t *testing.T) {
	host := &WindowsHost{Runner: fakePowerRunner{output: []byte(`{
		"cpu_model":"AMD EPYC 7B13",
		"cpu_cores":32,
		"cpu_usage":37.5,
		"memory_total_mb":65536,
		"memory_used_mb":32768,
		"disk_total_gb":1000,
		"disk_used_gb":250
	}`)}}

	stats, err := host.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.CPUModel != "AMD EPYC 7B13" || stats.CPUCores != 32 || stats.CPUUsage != 37.5 {
		t.Fatalf("unexpected cpu: %+v", stats)
	}
	if stats.MemoryUsage != 50 || stats.DiskUsage != 25 {
		t.Fatalf("unexpected usage: %+v", stats)
	}
}
