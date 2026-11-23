package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type PowerRunner interface {
	Run(ctx context.Context, script string) ([]byte, error)
}

type SystemPowerShell struct{}

func (SystemPowerShell) Run(ctx context.Context, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)

	return cmd.CombinedOutput()
}

type Windows struct {
	Interface string
	Runner    PowerRunner
}

func NewWindows(iface string) *Windows {
	return &Windows{Interface: iface, Runner: SystemPowerShell{}}
}

func (w *Windows) Read(ctx context.Context) (Counters, error) {
	script := `$ErrorActionPreference = 'Stop'
`
	if w.Interface != "" {
		script += fmt.Sprintf("Get-NetAdapterStatistics -Name '%s' | Select-Object Name,ReceivedBytes,SentBytes | ConvertTo-Json -Compress\n", strings.ReplaceAll(w.Interface, "'", "''"))
	} else {
		script += "Get-NetAdapterStatistics | Sort-Object ReceivedBytes -Descending | Select-Object -First 1 Name,ReceivedBytes,SentBytes | ConvertTo-Json -Compress\n"
	}

	output, err := w.Runner.Run(ctx, script)
	if err != nil {
		return Counters{}, fmt.Errorf("读取 Windows 网卡统计失败: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return ParseWindowsCounters(output)
}

func ParseWindowsCounters(payload []byte) (Counters, error) {
	var raw struct {
		Name          string      `json:"Name"`
		ReceivedBytes json.Number `json:"ReceivedBytes"`
		SentBytes     json.Number `json:"SentBytes"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Counters{}, err
	}

	rx, err := strconv.ParseUint(raw.ReceivedBytes.String(), 10, 64)
	if err != nil {
		return Counters{}, err
	}
	tx, err := strconv.ParseUint(raw.SentBytes.String(), 10, 64)
	if err != nil {
		return Counters{}, err
	}

	return Counters{Interface: raw.Name, RxBytes: rx, TxBytes: tx}, nil
}
