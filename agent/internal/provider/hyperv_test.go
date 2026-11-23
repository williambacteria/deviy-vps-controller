package provider

import (
	"context"
	"strings"
	"testing"
)

type fakePowerShell struct {
	outputs []string
	scripts []string
}

func (f *fakePowerShell) Run(_ context.Context, script string) ([]byte, error) {
	f.scripts = append(f.scripts, script)
	if len(f.outputs) == 0 {
		return []byte{}, nil
	}
	output := f.outputs[0]
	f.outputs = f.outputs[1:]

	return []byte(output), nil
}

func TestHyperVList(t *testing.T) {
	runner := &fakePowerShell{outputs: []string{`[{"id":"11111111-1111-1111-1111-111111111111","name":"web","status":"running","cores":2,"memory_mb":4096,"disk_gb":40,"node":"HV-01","uptime":3600}]`}}
	hyperv, err := NewHyperV(HyperVConfig{SwitchName: "External", Storage: `C:\VHDs`}, runner)
	if err != nil {
		t.Fatal(err)
	}

	vms, err := hyperv.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 1 || vms[0].ID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("unexpected vms: %+v", vms)
	}
	if !strings.Contains(runner.scripts[0], "Get-VM") {
		t.Fatalf("unexpected script: %s", runner.scripts[0])
	}
}

func TestHyperVCreateAndStatus(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	runner := &fakePowerShell{outputs: []string{
		id,
		`{"id":"11111111-1111-1111-1111-111111111111","name":"deviy-demo","status":"running","cores":2,"memory_mb":4096,"disk_gb":40,"node":"HV-01","uptime":0}`,
	}}
	hyperv, err := NewHyperV(HyperVConfig{SwitchName: "External", Storage: `C:\VHDs`}, runner)
	if err != nil {
		t.Fatal(err)
	}

	vm, err := hyperv.Create(context.Background(), CreateVMInput{
		Name: "deviy demo", Cores: 2, MemoryMB: 4096, DiskGB: 40, Start: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if vm.ID != id || vm.Status != "running" {
		t.Fatalf("unexpected vm: %+v", vm)
	}
	if !strings.Contains(runner.scripts[0], "New-VM") || !strings.Contains(runner.scripts[0], "Start-VM") {
		t.Fatalf("create script missing commands: %s", runner.scripts[0])
	}
	if !strings.Contains(runner.scripts[0], "deviy-demo") {
		t.Fatalf("create script did not sanitize name: %s", runner.scripts[0])
	}
}

func TestHyperVDiagnoseChecks(t *testing.T) {
	runner := &fakePowerShell{outputs: []string{"yes", "count=3", "switch=External", "ok"}}
	hyperv, err := NewHyperV(HyperVConfig{SwitchName: "External", Storage: `C:\VHDs`}, runner)
	if err != nil {
		t.Fatal(err)
	}

	checks := hyperv.Diagnose(context.Background())
	if len(checks) != 4 {
		t.Fatalf("expected 4 checks, got %d", len(checks))
	}
	for _, check := range checks {
		if check.Status != "ok" {
			t.Fatalf("check %s not ok: %s", check.Name, check.Message)
		}
	}
}

func TestHyperVActionsAndDelete(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	runner := &fakePowerShell{}
	hyperv, err := NewHyperV(HyperVConfig{}, runner)
	if err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"start", "stop", "reboot"} {
		if err := hyperv.Action(context.Background(), id, action); err != nil {
			t.Fatal(err)
		}
	}
	if err := hyperv.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(runner.scripts, "\n")
	for _, command := range []string{"Start-VM", "Stop-VM", "Restart-VM", "Remove-VM"} {
		if !strings.Contains(joined, command) {
			t.Fatalf("missing command %s in scripts", command)
		}
	}
}

func TestHyperVRejectsInvalidID(t *testing.T) {
	hyperv, err := NewHyperV(HyperVConfig{}, &fakePowerShell{})
	if err != nil {
		t.Fatal(err)
	}

	if err := hyperv.Action(context.Background(), "'; Remove-Item C:\\ -Recurse", "start"); err != ErrVMNotFound {
		t.Fatalf("expected ErrVMNotFound, got %v", err)
	}
}
