package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"deviy.cloud/agent/internal/provider"
)

const usageHistoryLimit = 720

type UsagePoint struct {
	At      string  `json:"at"`
	RxMbps  float64 `json:"rx_mbps"`
	TxMbps  float64 `json:"tx_mbps"`
	RxBytes uint64  `json:"rx_bytes"`
	TxBytes uint64  `json:"tx_bytes"`
	TotalRx uint64  `json:"total_rx_bytes"`
	TotalTx uint64  `json:"total_tx_bytes"`
}

type UsageRecord struct {
	VMID         string       `json:"vm_id"`
	CreatedAt    string       `json:"created_at"`
	TotalRxBytes uint64       `json:"total_rx_bytes"`
	TotalTxBytes uint64       `json:"total_tx_bytes"`
	LastRxBytes  uint64       `json:"-"`
	LastTxBytes  uint64       `json:"-"`
	LastSample   time.Time    `json:"-"`
	History      []UsagePoint `json:"history"`
}

type usageState struct {
	Records map[string]*UsageRecord `json:"records"`
}

type usageTracker struct {
	mu       sync.Mutex
	provider provider.Provider
	path     string
	records  map[string]*UsageRecord
}

func newUsageTracker(p provider.Provider, path string) *usageTracker {
	tracker := &usageTracker{provider: p, path: path, records: map[string]*UsageRecord{}}
	if path == "" {
		return tracker
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return tracker
	}
	var state usageState
	if json.Unmarshal(raw, &state) == nil {
		for id, record := range state.Records {
			if record != nil {
				tracker.records[id] = record
			}
		}
	}

	return tracker
}

func (u *usageTracker) Run() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	u.sample(context.Background())
	for range ticker.C {
		u.sample(context.Background())
	}
}

func (u *usageTracker) sample(parent context.Context) {
	statsProvider, ok := u.provider.(provider.StatsProvider)
	if !ok {
		return
	}
	list, err := u.provider.List(context.Background())
	if err != nil {
		return
	}
	now := time.Now()
	for _, vm := range list {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		stats, statsErr := statsProvider.Stats(ctx, vm.ID)
		cancel()
		if statsErr != nil {
			continue
		}
		u.mu.Lock()
		record := u.records[vm.ID]
		if record == nil {
			record = &UsageRecord{VMID: vm.ID, CreatedAt: now.Format(time.RFC3339)}
			u.records[vm.ID] = record
		}
		if !record.LastSample.IsZero() {
			deltaRx := uint64(0)
			deltaTx := uint64(0)
			if stats.NetRxBytes >= record.LastRxBytes {
				deltaRx = stats.NetRxBytes - record.LastRxBytes
			}
			if stats.NetTxBytes >= record.LastTxBytes {
				deltaTx = stats.NetTxBytes - record.LastTxBytes
			}
			elapsed := now.Sub(record.LastSample).Seconds()
			if elapsed <= 0 {
				elapsed = 1
			}
			record.TotalRxBytes += deltaRx
			record.TotalTxBytes += deltaTx
			record.History = append(record.History, UsagePoint{
				At:      now.Format(time.RFC3339),
				RxMbps:  roundMetric(float64(deltaRx) * 8 / elapsed / 1e6),
				TxMbps:  roundMetric(float64(deltaTx) * 8 / elapsed / 1e6),
				RxBytes: deltaRx,
				TxBytes: deltaTx,
				TotalRx: record.TotalRxBytes,
				TotalTx: record.TotalTxBytes,
			})
			if len(record.History) > usageHistoryLimit {
				record.History = record.History[len(record.History)-usageHistoryLimit:]
			}
		}
		record.LastRxBytes = stats.NetRxBytes
		record.LastTxBytes = stats.NetTxBytes
		record.LastSample = now
		u.mu.Unlock()
	}
	u.persist()
}

func (u *usageTracker) Get(id string) UsageRecord {
	u.mu.Lock()
	defer u.mu.Unlock()
	record := u.records[id]
	if record == nil {
		return UsageRecord{VMID: id, History: []UsagePoint{}}
	}
	copyRecord := *record
	copyRecord.History = append([]UsagePoint(nil), record.History...)
	return copyRecord
}

func (u *usageTracker) persist() {
	if u.path == "" {
		return
	}
	u.mu.Lock()
	state := usageState{Records: u.records}
	raw, err := json.Marshal(state)
	u.mu.Unlock()
	if err != nil {
		return
	}
	dir := filepath.Dir(u.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	temp := u.path + ".tmp"
	if err := os.WriteFile(temp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(temp, u.path)
}

func roundMetric(value float64) float64 {
	return float64(int(value*100+0.5)) / 100
}
