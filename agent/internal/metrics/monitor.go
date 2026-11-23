package metrics

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

const maxPoints = 120

type Monitor struct {
	collector Collector
	interval  time.Duration
	mu        sync.RWMutex
	last      *Counters
	points    []Point
	lastErr   string
}

func NewMonitor(collector Collector, interval time.Duration) *Monitor {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	monitor := &Monitor{collector: collector, interval: interval}
	monitor.Sample(context.Background())

	return monitor
}

func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Sample(ctx)
		}
	}
}

func (m *Monitor) Sample(ctx context.Context) Snapshot {
	counter, err := m.collector.Read(ctx)
	if err != nil {
		m.mu.Lock()
		m.lastErr = err.Error()
		snapshot := m.snapshotLocked()
		m.mu.Unlock()

		return snapshot
	}

	now := time.Now()
	counter.Timestamp = now

	m.mu.Lock()
	defer m.mu.Unlock()

	rxMbps, txMbps := 0.0, 0.0
	if m.last != nil && !m.last.Timestamp.IsZero() {
		seconds := now.Sub(m.last.Timestamp).Seconds()
		if seconds > 0 && counter.RxBytes >= m.last.RxBytes && counter.TxBytes >= m.last.TxBytes {
			rxMbps = round2(float64(counter.RxBytes-m.last.RxBytes) * 8 / 1_000_000 / seconds)
			txMbps = round2(float64(counter.TxBytes-m.last.TxBytes) * 8 / 1_000_000 / seconds)
		}
	}

	m.last = &counter
	m.lastErr = ""
	m.points = append(m.points, Point{
		Time:   now.Format("15:04:05"),
		RxMbps: rxMbps,
		TxMbps: txMbps,
	})
	if len(m.points) > maxPoints {
		m.points = m.points[len(m.points)-maxPoints:]
	}

	return m.snapshotLocked()
}

func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.snapshotLocked()
}

func (m *Monitor) snapshotLocked() Snapshot {
	snapshot := Snapshot{
		Interface: "unknown",
		Unit:      "Mbps",
		Points:    append([]Point(nil), m.points...),
		Error:     m.lastErr,
	}
	if m.last != nil {
		snapshot.Interface = m.last.Interface
		snapshot.TotalRx = formatBytes(m.last.RxBytes)
		snapshot.TotalTx = formatBytes(m.last.TxBytes)
		snapshot.UpdatedAt = m.last.Timestamp.Format(time.RFC3339)
	}
	if len(m.points) > 0 {
		snapshot.RxMbps = m.points[len(m.points)-1].RxMbps
		snapshot.TxMbps = m.points[len(m.points)-1].TxMbps
	}

	return snapshot
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	units := []string{"KB", "MB", "GB", "TB", "PB"}
	value := float64(bytes)
	unitIndex := -1
	for value >= unit && unitIndex < len(units)-1 {
		value /= unit
		unitIndex++
	}

	return fmt.Sprintf("%.2f %s", value, units[unitIndex])
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}
