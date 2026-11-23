package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPVEListAndCreate(t *testing.T) {
	var createForm map[string][]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Header.Get("Authorization") != "PVEAPIToken=user@pve!deviy=secret" {
			t.Fatalf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve/qemu":
			_, _ = w.Write([]byte(`{"data":[{"vmid":100,"name":"web","status":"running","cpus":2,"maxmem":4294967296,"maxdisk":42949672960,"uptime":3600}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/cluster/nextid":
			_, _ = w.Write([]byte(`{"data":"120"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api2/json/nodes/pve/qemu":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			createForm = r.PostForm
			_, _ = w.Write([]byte(`{"data":"UPID:pve:0001:create:120:root@pam:"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve/qemu/120/status/current":
			_, _ = w.Write([]byte(`{"data":{"vmid":120,"name":"new-vm","status":"running","cpus":2,"maxmem":4294967296,"maxdisk":42949672960,"uptime":0}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/status/start"):
			_, _ = w.Write([]byte(`{"data":"UPID:pve:0002:start:120:root@pam:"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			_, _ = w.Write([]byte(`{"data":{"scsi0":"local-lvm:vm-100-disk-0,size=40G"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	pve, err := NewPVE(PVEConfig{
		BaseURL: server.URL,
		Token:   "user@pve!deviy=secret",
		Node:    "pve",
		Storage: "local-lvm",
		Bridge:  "vmbr1",
	})
	if err != nil {
		t.Fatal(err)
	}

	vms, err := pve.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 1 || vms[0].VMID != 100 || vms[0].MemoryMB != 4096 {
		t.Fatalf("unexpected vms: %+v", vms)
	}

	vm, err := pve.Create(context.Background(), CreateVMInput{
		Name: "new-vm", Cores: 2, MemoryMB: 4096, DiskGB: 40, DataDiskGB: 20, Start: true,
		IP: "10.0.0.5", Gateway: "10.0.0.1", PrefixLen: 24,
		SystemDiskMinMBps: 100, SystemDiskMaxMBps: 200,
		DataDiskMinMBps: 50, DataDiskMaxMBps: 80,
		DNS1: "223.5.5.5", DNS2: "223.6.6.6",
		ISOFile: "local:iso/debian-12.iso",
	})
	if err != nil {
		t.Fatal(err)
	}
	if vm.VMID != 120 || vm.Name != "new-vm" {
		t.Fatalf("unexpected vm: %+v", vm)
	}
	if formValue(createForm, "vmid") != "120" || formValue(createForm, "net0") != "virtio,bridge=vmbr1,ip=10.0.0.5/24,gw=10.0.0.1" {
		t.Fatalf("unexpected create form: %+v", createForm)
	}
	if formValue(createForm, "scsi0") != "local-lvm:40,mbps_rd=100,mbps_wr=100,mbps_rd_max=200,mbps_wr_max=200" {
		t.Fatalf("unexpected disk: %s", formValue(createForm, "scsi0"))
	}
	if formValue(createForm, "scsi1") != "local-lvm:20,mbps_rd=50,mbps_wr=50,mbps_rd_max=80,mbps_wr_max=80" {
		t.Fatalf("unexpected data disk: %s", formValue(createForm, "scsi1"))
	}
	if formValue(createForm, "nameserver") != "223.5.5.5 223.6.6.6" {
		t.Fatalf("unexpected nameserver: %s", formValue(createForm, "nameserver"))
	}
	if formValue(createForm, "ide2") != "local:iso/debian-12.iso,media=cdrom" {
		t.Fatalf("unexpected iso: %s", formValue(createForm, "ide2"))
	}
}

func TestPVECreateFromQCOW2ImportsDisk(t *testing.T) {
	image := filepath.Join(t.TempDir(), "debian-12.qcow2")
	if err := os.WriteFile(image, []byte("qcow2"), 0o600); err != nil {
		t.Fatal(err)
	}

	var createForm map[string][]string
	var configForm map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/cluster/nextid":
			_, _ = w.Write([]byte(`{"data":"122"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api2/json/nodes/pve/qemu":
			_ = r.ParseForm()
			createForm = r.PostForm
			_, _ = w.Write([]byte(`{"data":"UPID:pve:create:122"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api2/json/nodes/pve/qemu/122/config":
			_ = r.ParseForm()
			configForm = r.PostForm
			_, _ = w.Write([]byte(`{"data":"UPID:pve:config:122"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve/qemu/122/status/current":
			_, _ = w.Write([]byte(`{"data":{"vmid":122,"name":"from-image","status":"stopped","cpus":2,"maxmem":4294967296,"maxdisk":42949672960}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			_, _ = w.Write([]byte(`{"data":{"scsi0":"local-lvm:vm-122-disk-0,size=40G"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	pve, err := NewPVE(PVEConfig{BaseURL: server.URL, Token: "t", Node: "pve", Storage: "local-lvm"})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakePVECommandRunner{output: []byte("Successfully imported disk as 'unused0:local-lvm:vm-122-disk-0'")}
	pve.Runner = runner

	vm, err := pve.Create(context.Background(), CreateVMInput{
		Name: "from-image", Image: image, Storage: "local-lvm",
		SystemDiskMinMBps: 100, SystemDiskMaxMBps: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if vm.VMID != 122 || formValue(createForm, "scsi0") != "" {
		t.Fatalf("unexpected VM/create form: %+v %+v", vm, createForm)
	}
	if len(runner.args) < 6 || runner.args[0] != "importdisk" || runner.args[1] != "122" || runner.args[2] != image {
		t.Fatalf("unexpected qm args: %+v", runner.args)
	}
	expectedDisk := "local-lvm:vm-122-disk-0,mbps_rd=100,mbps_wr=100,mbps_rd_max=200,mbps_wr_max=200"
	if formValue(configForm, "scsi0") != expectedDisk {
		t.Fatalf("unexpected scsi0: %s", formValue(configForm, "scsi0"))
	}
}

type fakePVECommandRunner struct {
	output []byte
	args   []string
}

func (f *fakePVECommandRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.args = args

	return f.output, nil
}

func TestPVECreateFromTemplate(t *testing.T) {
	var cloneForm map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/cluster/nextid":
			_, _ = w.Write([]byte(`{"data":"121"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api2/json/nodes/pve/qemu/9000/clone":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			cloneForm = r.PostForm
			_, _ = w.Write([]byte(`{"data":"UPID:pve:clone:121"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve/qemu/121/status/current":
			_, _ = w.Write([]byte(`{"data":{"vmid":121,"name":"cloned","status":"stopped","cpus":2,"maxmem":4294967296,"maxdisk":42949672960}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			_, _ = w.Write([]byte(`{"data":{"scsi0":"local-lvm:vm-121-disk-0,size=40G"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	pve, err := NewPVE(PVEConfig{BaseURL: server.URL, Token: "t", Node: "pve"})
	if err != nil {
		t.Fatal(err)
	}

	vm, err := pve.Create(context.Background(), CreateVMInput{Name: "cloned", Template: "9000"})
	if err != nil {
		t.Fatal(err)
	}
	if vm.VMID != 121 || formValue(cloneForm, "newid") != "121" || formValue(cloneForm, "full") != "1" {
		t.Fatalf("unexpected clone: %+v / %+v", vm, cloneForm)
	}
}

func TestPVEDiagnoseChecksNodeStorageAndBridge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes":
			_, _ = w.Write([]byte(`{"data":[{"node":"pve","status":"online"}]}`))
		case "/api2/json/nodes/pve/storage":
			_, _ = w.Write([]byte(`{"data":[{"storage":"local-lvm"},{"storage":"local"}]}`))
		case "/api2/json/nodes/pve/network":
			_, _ = w.Write([]byte(`{"data":[{"iface":"vmbr0"},{"iface":"vmbr1"}]}`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	pve, err := NewPVE(PVEConfig{BaseURL: server.URL, Token: "token", Node: "pve", Storage: "local-lvm", Bridge: "vmbr1"})
	if err != nil {
		t.Fatal(err)
	}

	checks := pve.Diagnose(context.Background())
	if len(checks) != 4 {
		t.Fatalf("expected 4 checks, got %d", len(checks))
	}
	for _, check := range checks {
		if check.Status != "ok" {
			t.Fatalf("check %s not ok: %s", check.Name, check.Message)
		}
	}
}

func formValue(form map[string][]string, key string) string {
	if len(form[key]) == 0 {
		return ""
	}

	return form[key][0]
}

func TestPVEBackupUsesStorage(t *testing.T) {
	var form map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api2/json/nodes/pve/vzdump" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		form = r.PostForm
		_, _ = w.Write([]byte(`{"data":"UPID:pve:backup:120"}`))
	}))
	defer server.Close()

	pve, err := NewPVE(PVEConfig{BaseURL: server.URL, Token: "t", Node: "pve", Storage: "local-lvm"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := pve.Backup(context.Background(), "120", "backup-store")
	if err != nil {
		t.Fatal(err)
	}
	if result == "" || formValue(form, "storage") != "backup-store" || formValue(form, "vmid") != "120" {
		t.Fatalf("unexpected backup: %s %+v", result, form)
	}
}

func TestPVEStatusNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":"not found"}`))
	}))
	defer server.Close()

	pve, err := NewPVE(PVEConfig{BaseURL: server.URL, Token: "t", Node: "pve"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = pve.Status(context.Background(), "999")
	if err != ErrVMNotFound {
		t.Fatalf("expected ErrVMNotFound, got %v", err)
	}
}

func TestPVEConfigValidation(t *testing.T) {
	if _, err := NewPVE(PVEConfig{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestPVECreateReturnsJSONDecodeErrorForBadPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/cluster/nextid" {
			_, _ = w.Write([]byte(`{"data":"120"}`))
			return
		}
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()

	pve, err := NewPVE(PVEConfig{BaseURL: server.URL, Token: "t", Node: "pve"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pve.Create(context.Background(), CreateVMInput{Name: "x"}); err == nil {
		t.Fatal("expected json error")
	}
}
