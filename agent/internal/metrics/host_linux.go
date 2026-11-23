//go:build linux

package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type LinuxHost struct {
	mu        sync.Mutex
	prevIdle  uint64
	prevTotal uint64
}

func NewLinuxHost() *LinuxHost {
	return &LinuxHost{}
}

func newPlatformHost() HostCollector {
	return NewLinuxHost()
}

func (l *LinuxHost) Read(_ context.Context) (HostStats, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	model, cores := cpuInfo()
	cpuUsage := l.cpuUsage()
	memoryTotal, memoryUsed := memoryInfo()
	disks := diskList()
	diskTotal, diskUsed := 0.0, 0.0
	for _, disk := range disks {
		diskTotal += disk.TotalGB
		diskUsed += disk.UsedGB
	}
	if diskTotal == 0 {
		diskTotal, diskUsed = diskInfo("/")
	}

	stats := finalizeHost(HostStats{
		CPUModel:      model,
		CPUCores:      cores,
		CPUUsage:      cpuUsage,
		MemoryTotalMB: memoryTotal,
		MemoryUsedMB:  memoryUsed,
		DiskTotalGB:   diskTotal,
		DiskUsedGB:    diskUsed,
		Disks:         disks,
	})
	stats.UpdatedAt = time.Now().Format(time.RFC3339)

	return stats, nil
}

func (l *LinuxHost) cpuUsage() float64 {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	lines := strings.Split(string(raw), "\n")
	fields := strings.Fields(lines[0])
	if len(fields) < 5 {
		return 0
	}
	var total, idle uint64
	for index, field := range fields[1:] {
		value, _ := strconv.ParseUint(field, 10, 64)
		total += value
		if index == 3 || index == 4 {
			idle += value
		}
	}
	deltaTotal := total - l.prevTotal
	deltaIdle := idle - l.prevIdle
	l.prevTotal = total
	l.prevIdle = idle
	if deltaTotal == 0 || deltaIdle > deltaTotal {
		return 0
	}

	return float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
}

func cpuInfo() (string, int) {
	raw, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "", 0
	}
	model := ""
	cores := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "processor") {
			cores++
		}
		if model == "" && strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				model = strings.TrimSpace(parts[1])
			}
		}
	}

	return model, cores
}

func memoryInfo() (int64, int64) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	values := map[string]int64{}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		value, _ := strconv.ParseInt(parts[1], 10, 64)
		values[strings.TrimSuffix(parts[0], ":")] = value
	}
	total := values["MemTotal"] / 1024
	available := values["MemAvailable"] / 1024
	if available == 0 {
		available = (values["MemFree"] + values["Buffers"] + values["Cached"]) / 1024
	}
	used := total - available
	if used < 0 {
		used = 0
	}

	return total, used
}

func diskInfo(path string) (float64, float64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	total := float64(stat.Blocks) * float64(stat.Bsize) / (1024 * 1024 * 1024)
	available := float64(stat.Bavail) * float64(stat.Bsize) / (1024 * 1024 * 1024)

	return total, total - available
}

func diskList() []HostDisk {
	raw, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	disks := make([]HostDisk, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.HasPrefix(fields[0], "/dev/") {
			continue
		}
		device := fields[0]
		if seen[device] {
			continue
		}
		base := filepath.Base(device)
		if strings.HasPrefix(base, "loop") || strings.HasPrefix(base, "ram") || strings.HasPrefix(base, "fd") || strings.HasPrefix(base, "sr") {
			continue
		}
		mount := strings.ReplaceAll(fields[1], "\\040", " ")
		if mount == "/boot" || mount == "/boot/efi" {
			continue
		}
		seen[device] = true
		total, used := diskInfo(fields[1])
		if total <= 0 {
			continue
		}
		disks = append(disks, HostDisk{
			Device:  device,
			Name:    filepath.Base(device),
			Mount:   mount,
			TotalGB: round2(total),
			UsedGB:  round2(used),
		})
	}

	return disks
}
