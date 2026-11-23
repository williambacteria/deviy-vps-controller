package metrics

import (
	"context"
	"time"
)

type Counters struct {
	Interface string
	RxBytes   uint64
	TxBytes   uint64
	Timestamp time.Time
}

type Collector interface {
	Read(ctx context.Context) (Counters, error)
}

type Point struct {
	Time   string  `json:"time"`
	RxMbps float64 `json:"rx_mbps"`
	TxMbps float64 `json:"tx_mbps"`
}

type Snapshot struct {
	Interface string  `json:"interface"`
	Unit      string  `json:"unit"`
	RxMbps    float64 `json:"rx_mbps"`
	TxMbps    float64 `json:"tx_mbps"`
	TotalRx   string  `json:"total_rx"`
	TotalTx   string  `json:"total_tx"`
	Points    []Point `json:"points"`
	UpdatedAt string  `json:"updated_at"`
	Error     string  `json:"error,omitempty"`
}
