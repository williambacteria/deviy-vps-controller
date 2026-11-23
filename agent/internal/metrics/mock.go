package metrics

import (
	"context"
	"sync/atomic"
)

type Mock struct {
	forwarded atomic.Uint64
}

func NewMock() *Mock {
	return &Mock{}
}

func (m *Mock) Read(_ context.Context) (Counters, error) {
	step := m.forwarded.Add(1)

	return Counters{
		Interface: "vmbr0",
		RxBytes:   128_000_000_000 + step*1_500_000,
		TxBytes:   42_000_000_000 + step*420_000,
	}, nil
}
