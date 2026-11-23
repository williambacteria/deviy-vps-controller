package metrics

import (
	"context"
	"fmt"
)

type HostDisk struct {
	Device  string  `json:"device"`
	Name    string  `json:"name"`
	Mount   string  `json:"mount"`
	TotalGB float64 `json:"total_gb"`
	UsedGB  float64 `json:"used_gb"`
	Usage   float64 `json:"usage"`
}

type HostStats struct {
	CPUModel      string     `json:"cpu_model"`
	CPUCores      int        `json:"cpu_cores"`
	CPUUsage      float64    `json:"cpu_usage"`
	MemoryTotalMB int64      `json:"memory_total_mb"`
	MemoryUsedMB  int64      `json:"memory_used_mb"`
	MemoryUsage   float64    `json:"memory_usage"`
	DiskTotalGB   float64    `json:"disk_total_gb"`
	DiskUsedGB    float64    `json:"disk_used_gb"`
	DiskUsage     float64    `json:"disk_usage"`
	Disks         []HostDisk `json:"disks,omitempty"`
	UpdatedAt     string     `json:"updated_at"`
}

type HostCollector interface {
	Read(ctx context.Context) (HostStats, error)
}

func NewHostCollector() HostCollector {
	return newPlatformHost()
}

type mockHostCollector struct{}

func (m *mockHostCollector) Read(_ context.Context) (HostStats, error) {
	return finalizeHost(HostStats{
		CPUModel:      "Mock CPU",
		CPUCores:      8,
		CPUUsage:      12.5,
		MemoryTotalMB: 16384,
		MemoryUsedMB:  4096,
		DiskTotalGB:   500,
		DiskUsedGB:    120,
		Disks: []HostDisk{
			{Device: "mock0", Name: "vda", Mount: "/", TotalGB: 500, UsedGB: 120},
		},
	}), nil
}

func finalizeHost(stats HostStats) HostStats {
	if stats.CPUCores < 0 {
		stats.CPUCores = 0
	}
	if stats.MemoryTotalMB > 0 {
		stats.MemoryUsage = round2(float64(stats.MemoryUsedMB) / float64(stats.MemoryTotalMB) * 100)
	}
	if len(stats.Disks) > 0 {
		stats.DiskTotalGB = 0
		stats.DiskUsedGB = 0
		for index := range stats.Disks {
			if stats.Disks[index].TotalGB > 0 {
				stats.Disks[index].Usage = round2(stats.Disks[index].UsedGB / stats.Disks[index].TotalGB * 100)
			}
			stats.DiskTotalGB += stats.Disks[index].TotalGB
			stats.DiskUsedGB += stats.Disks[index].UsedGB
		}
	}
	if stats.DiskTotalGB > 0 {
		stats.DiskUsage = round2(stats.DiskUsedGB / stats.DiskTotalGB * 100)
	}
	stats.CPUUsage = round2(stats.CPUUsage)

	return stats
}

func hostError(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
