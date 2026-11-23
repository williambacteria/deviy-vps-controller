package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVMProxyAllocatesAndReleasesIP(t *testing.T) {
	var createBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/vms":
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"vm-1","name":"vm-one","status":"running"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/vms/vm-1/action":
			_, _ = w.Write([]byte(`{"status":"deleted"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/nat":
			_, _ = w.Write([]byte(`{"items":[]}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer upstream.Close()

	app := NewWithOptions([]Node{{
		ID: "node-1", Name: "Node 1", Provider: "pve", Region: "香港", URL: upstream.URL,
		PublicBridge: "vmbr0", PrivateBridge: "vmbr1",
		SystemDiskMinMBps: 100, SystemDiskMaxMBps: 200,
		DataDiskMinMBps: 50, DataDiskMaxMBps: 80,
		TemplateDir: "template", SystemDiskDir: "os", DataDiskDir: "data",
		ISODir: "iso", BackupDir: "backup", DNS1: "223.5.5.5", DNS2: "223.6.6.6",
		PortMin: 10000, PortMax: 30000, ExcludePorts: "25565,17375",
	}}, nil, false, "")
	if err := app.LoadState(t.TempDir() + "/state.json"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	pool := `{"name":"HK-Public","node_id":"node-1","region":"香港","type":"public","cidr":"203.0.113.0/30","gateway":"203.0.113.1","start":"203.0.113.2","end":"203.0.113.2"}`
	resp, err := http.Post(server.URL+"/api/ip-pools", "application/json", strings.NewReader(pool))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create pool status = %d", resp.StatusCode)
	}

	request := `{"name":"vm-one","ip_pool_id":"` + poolID(t, server.URL) + `"}`
	resp, err = http.Post(server.URL+"/api/nodes/node-1/vms", "application/json", strings.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create vm status = %d", resp.StatusCode)
	}
	if createBody["ip"] != "203.0.113.2" || createBody["gateway"] != "203.0.113.1" {
		t.Fatalf("unexpected create body: %+v", createBody)
	}
	if createBody["bridge"] != "vmbr0" {
		t.Fatalf("public network should use public bridge: %+v", createBody)
	}
	if createBody["system_disk_min_mbps"] != float64(100) || createBody["dns1"] != "223.5.5.5" || createBody["port_min"] != float64(10000) {
		t.Fatalf("host resource settings not forwarded: %+v", createBody)
	}

	app.mu.RLock()
	allocated := len(app.ipPools[0].Allocations)
	app.mu.RUnlock()
	if allocated != 1 {
		t.Fatalf("allocations = %d", allocated)
	}

	resp, err = http.Post(server.URL+"/api/nodes/node-1/vms/vm-1/action", "application/json", strings.NewReader(`{"action":"delete"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}

	app.mu.RLock()
	allocated = len(app.ipPools[0].Allocations)
	app.mu.RUnlock()
	if allocated != 0 {
		t.Fatalf("allocations after delete = %d", allocated)
	}
}

func TestPrivateVMCreateAllocatesNATPort(t *testing.T) {
	var natBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/vms":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"vm-private"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/nat/allocate":
			if err := json.NewDecoder(r.Body).Decode(&natBody); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"external_port":10000}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer upstream.Close()

	app := NewWithOptions([]Node{{
		ID: "node-1", Name: "Node 1", Provider: "pve", Region: "香港", URL: upstream.URL,
		NodeIP: "203.0.113.200", PrivateBridge: "vmbr1",
		PortMin: 10000, PortMax: 10100, ExcludePorts: "10001",
	}}, nil, false, "")
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	pool := `{"name":"Private","type":"private","region":"global","cidr":"10.0.0.0/30","gateway":"10.0.0.1","start":"10.0.0.2","end":"10.0.0.2"}`
	resp, err := http.Post(server.URL+"/api/ip-pools", "application/json", strings.NewReader(pool))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	poolIDValue := poolID(t, server.URL)
	request := `{"name":"vm-private","network_mode":"private","ip_pool_id":"` + poolIDValue + `"}`
	resp, err = http.Post(server.URL+"/api/nodes/node-1/vms", "application/json", strings.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create vm status = %d", resp.StatusCode)
	}
	if natBody["external_ip"] != "203.0.113.200" || natBody["internal_ip"] != "10.0.0.2" {
		t.Fatalf("unexpected nat request: %+v", natBody)
	}
	if natBody["port_min"] != float64(10000) || natBody["exclude_ports"] != "10001" {
		t.Fatalf("unexpected nat range: %+v", natBody)
	}
}

func TestPrepareVMCreateUsesPrivateBridge(t *testing.T) {
	app := NewWithOptions([]Node{{ID: "node-1", PrivateBridge: "vmbr1"}}, nil, false, "")
	body, _, _, err := app.prepareVMCreate([]byte(`{"network_mode":"private"}`), app.nodes["node-1"])
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["bridge"] != "vmbr1" {
		t.Fatalf("private network bridge = %v", payload["bridge"])
	}
}

func poolID(t *testing.T, baseURL string) string {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/ip-pools")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var data struct {
		Items []IPPool `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	if len(data.Items) != 1 {
		t.Fatalf("pools = %d", len(data.Items))
	}

	return data.Items[0].ID
}
