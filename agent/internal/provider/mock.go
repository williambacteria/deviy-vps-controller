package provider

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Mock struct {
	mu  sync.Mutex
	vms map[string]VM
	seq int
}

func NewMock() *Mock {
	return &Mock{
		vms: map[string]VM{
			"101": {ID: "101", VMID: 101, Name: "web-prod-01", Status: "running", Cores: 2, MemoryMB: 4096, DiskGB: 40, IP: "203.0.113.21", Node: "mock", Uptime: 86400},
			"102": {ID: "102", VMID: 102, Name: "nat-dev-01", Status: "running", Cores: 1, MemoryMB: 1024, DiskGB: 20, IP: "10.0.0.12", Node: "mock", Uptime: 3600},
			"103": {ID: "103", VMID: 103, Name: "game-us-01", Status: "stopped", Cores: 4, MemoryMB: 8192, DiskGB: 80, IP: "198.51.100.42", Node: "mock", Uptime: 0},
		},
		seq: 110,
	}
}

func (m *Mock) Name() string {
	return "mock"
}

func (m *Mock) Diagnose(_ context.Context) []Diagnostic {
	return []Diagnostic{
		{Name: "provider.mock", Status: "ok", Message: "Mock Provider 正常，无需真实宿主机"},
	}
}

func (m *Mock) List(_ context.Context) ([]VM, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	vms := make([]VM, 0, len(m.vms))
	for _, vm := range m.vms {
		vms = append(vms, vm)
	}
	sort.Slice(vms, func(i, j int) bool {
		return vms[i].VMID < vms[j].VMID
	})

	return vms, nil
}

func (m *Mock) Create(_ context.Context, input CreateVMInput) (VM, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.seq++
	status := "stopped"
	if input.Start {
		status = "running"
	}
	vm := VM{
		ID:           strconv.Itoa(m.seq),
		VMID:         m.seq,
		Name:         input.Name,
		Status:       status,
		Cores:        input.Cores,
		MemoryMB:     input.MemoryMB,
		DiskGB:       input.DiskGB + input.DataDiskGB,
		SystemDiskGB: input.DiskGB,
		DataDiskGB:   input.DataDiskGB,
		Node:         "mock",
		Uptime:       0,
	}
	if status == "running" {
		vm.Uptime = int64(time.Minute.Seconds())
	}
	m.vms[vm.ID] = vm

	return vm, nil
}

func (m *Mock) Status(_ context.Context, id string) (VM, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, ok := m.vms[id]
	if !ok {
		return VM{}, ErrVMNotFound
	}

	return vm, nil
}

func (m *Mock) Action(_ context.Context, id string, action string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, ok := m.vms[id]
	if !ok {
		return ErrVMNotFound
	}

	switch action {
	case "start":
		vm.Status = "running"
		if vm.Uptime == 0 {
			vm.Uptime = int64(time.Minute.Seconds())
		}
	case "stop", "shutdown":
		vm.Status = "stopped"
		vm.Uptime = 0
	case "reboot":
		vm.Status = "running"
		vm.Uptime = 0
	default:
		return errors.New("unsupported action")
	}

	m.vms[id] = vm

	return nil
}

func (m *Mock) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.vms[id]; !ok {
		return ErrVMNotFound
	}
	delete(m.vms, id)

	return nil
}
