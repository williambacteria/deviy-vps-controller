package metrics

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type ProcNetDev struct {
	Interface string
	Path      string
}

func NewProcNetDev(iface string) *ProcNetDev {
	return &ProcNetDev{Interface: iface, Path: "/proc/net/dev"}
}

func (p *ProcNetDev) Read(_ context.Context) (Counters, error) {
	payload, err := os.ReadFile(p.Path)
	if err != nil {
		return Counters{}, fmt.Errorf("读取 %s 失败: %w", p.Path, err)
	}

	return ParseProcNetDev(string(payload), p.Interface)
}

func ParseProcNetDev(payload string, wanted string) (Counters, error) {
	lines := strings.Split(payload, "\n")
	if len(lines) < 3 {
		return Counters{}, fmt.Errorf("/proc/net/dev 内容不完整")
	}

	var fallback *Counters
	for _, line := range lines[2:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(fields[0], 10, 64)
		tx, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}

		counter := Counters{Interface: name, RxBytes: rx, TxBytes: tx}
		if name == wanted {
			return counter, nil
		}
		if fallback == nil && name != "lo" {
			fallback = &counter
		}
	}

	if fallback != nil {
		return *fallback, nil
	}

	return Counters{}, fmt.Errorf("未找到网卡 %s 的流量计数", wanted)
}
