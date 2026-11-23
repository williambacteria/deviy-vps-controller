package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type WindowsHost struct {
	Runner PowerRunner
}

func NewWindowsHost() *WindowsHost {
	return &WindowsHost{Runner: SystemPowerShell{}}
}

func newPlatformHost() HostCollector {
	return NewWindowsHost()
}

func (w *WindowsHost) Read(ctx context.Context) (HostStats, error) {
	if w.Runner == nil {
		w.Runner = SystemPowerShell{}
	}

	script := `$ErrorActionPreference = 'Stop'
$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
$os = Get-CimInstance Win32_OperatingSystem
$disks = @(Get-CimInstance Win32_LogicalDisk -Filter "DriveType=3")
$totalDisk = ($disks | Measure-Object -Property Size -Sum).Sum
$freeDisk = ($disks | Measure-Object -Property FreeSpace -Sum).Sum
$diskItems = @($disks | ForEach-Object {
  [pscustomobject]@{
    device = [string]$_.DeviceID
    name = [string]$_.DeviceID
    mount = [string]$_.DeviceID
    total_gb = [double]($_.Size / 1GB)
    used_gb = [double](($_.Size - $_.FreeSpace) / 1GB)
  }
})
$totalMem = [double]$os.TotalVisibleMemorySize
$freeMem = [double]$os.FreePhysicalMemory
[pscustomobject]@{
  cpu_model = [string]$cpu.Name
  cpu_cores = [int]$cpu.NumberOfLogicalProcessors
  cpu_usage = [double]$cpu.LoadPercentage
  memory_total_mb = [int64]($totalMem / 1KB)
  memory_used_mb = [int64](($totalMem - $freeMem) / 1KB)
  disk_total_gb = [double]($totalDisk / 1GB)
  disk_used_gb = [double](($totalDisk - $freeDisk) / 1GB)
  disks = $diskItems
} | ConvertTo-Json -Compress -Depth 5
`

	output, err := w.Runner.Run(ctx, script)
	if err != nil {
		return HostStats{}, fmt.Errorf("读取 Windows 主机指标失败: %w: %s", err, strings.TrimSpace(string(output)))
	}

	var raw struct {
		CPUModel      string      `json:"cpu_model"`
		CPUCores      json.Number `json:"cpu_cores"`
		CPUUsage      json.Number `json:"cpu_usage"`
		MemoryTotalMB json.Number `json:"memory_total_mb"`
		MemoryUsedMB  json.Number `json:"memory_used_mb"`
		DiskTotalGB   json.Number `json:"disk_total_gb"`
		DiskUsedGB    json.Number `json:"disk_used_gb"`
		Disks         []struct {
			Device  string      `json:"device"`
			Name    string      `json:"name"`
			Mount   string      `json:"mount"`
			TotalGB json.Number `json:"total_gb"`
			UsedGB  json.Number `json:"used_gb"`
		} `json:"disks"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return HostStats{}, fmt.Errorf("解析 Windows 主机指标失败: %w", err)
	}

	cores, _ := strconv.Atoi(raw.CPUCores.String())
	cpuUsage, _ := strconv.ParseFloat(raw.CPUUsage.String(), 64)
	memoryTotal, _ := strconv.ParseInt(raw.MemoryTotalMB.String(), 10, 64)
	memoryUsed, _ := strconv.ParseInt(raw.MemoryUsedMB.String(), 10, 64)
	diskTotal, _ := strconv.ParseFloat(raw.DiskTotalGB.String(), 64)
	diskUsed, _ := strconv.ParseFloat(raw.DiskUsedGB.String(), 64)
	disks := make([]HostDisk, 0, len(raw.Disks))
	for _, item := range raw.Disks {
		total, _ := strconv.ParseFloat(item.TotalGB.String(), 64)
		used, _ := strconv.ParseFloat(item.UsedGB.String(), 64)
		disks = append(disks, HostDisk{Device: item.Device, Name: item.Name, Mount: item.Mount, TotalGB: total, UsedGB: used})
	}

	stats := finalizeHost(HostStats{
		CPUModel:      strings.TrimSpace(raw.CPUModel),
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
