package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"deviy.cloud/agent/internal/network"
)

type driftEngine struct {
	verified bool
	applied  int
}

func (d *driftEngine) Name() string { return "fake" }

func (d *driftEngine) PlanNAT(externalIP string, rules []network.NATRule) (network.Plan, error) {
	return network.Plan{Backend: "fake", Kind: "nat", Summary: "fake"}, nil
}

func (d *driftEngine) PlanFirewall(rules []network.FirewallRule) (network.Plan, error) {
	return network.Plan{Backend: "fake", Kind: "firewall"}, nil
}

func (d *driftEngine) Apply(_ context.Context, _ network.Plan) error {
	d.applied++
	d.verified = true

	return nil
}

func (d *driftEngine) VerifyNAT(_ context.Context, _ string, _ []network.NATRule) (network.Verification, error) {
	return network.Verification{Backend: "fake", Verified: d.verified}, nil
}

func TestNATAllocateUsesRangeAndExclusions(t *testing.T) {
	app := New(Config{Version: "test", Provider: "pve", NodeName: "node-a"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	body := `{"external_ip":"198.51.100.10","internal_ip":"10.0.0.12","internal_port":22,"protocol":"tcp","port_min":20000,"port_max":20002,"exclude_ports":"20001"}`
	post := func() *http.Response {
		resp, err := http.Post(server.URL+"/api/nat/allocate", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := post()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first allocate status = %d", resp.StatusCode)
	}
	var first NATRule
	if err := json.NewDecoder(resp.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	if first.ExternalPort != 20000 {
		t.Fatalf("first port = %d", first.ExternalPort)
	}

	resp = post()
	defer resp.Body.Close()
	var second NATRule
	if err := json.NewDecoder(resp.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	if second.ExternalPort != 20002 {
		t.Fatalf("second port = %d", second.ExternalPort)
	}

	resp = post()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("exhausted status = %d", resp.StatusCode)
	}
}

func TestISOListReturnsISOImages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "debian-12.iso"), []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore"), 0o600); err != nil {
		t.Fatal(err)
	}

	app := New(Config{Version: "test", Provider: "pve", NodeName: "node-a"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/isos?dir=" + url.QueryEscape(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Items []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Name != "debian-12.iso" {
		t.Fatalf("unexpected isos: %+v", payload.Items)
	}
}

func TestNATAllocateAppliesWhenEnabled(t *testing.T) {
	app := New(Config{Version: "test", Provider: "hyperv", Mode: "mock", NetworkBackend: "mock", AllowNetworkApply: true})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	body := `{"external_ip":"203.0.113.88","internal_ip":"10.0.0.50","internal_port":22,"protocol":"tcp","port_min":22000,"port_max":22010}`
	resp, err := http.Post(server.URL+"/api/nat/allocate", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var rule NATRule
	if err := json.NewDecoder(resp.Body).Decode(&rule); err != nil {
		t.Fatal(err)
	}
	if rule.Status != "active" || rule.ExternalPort != 22000 {
		t.Fatalf("unexpected allocated rule: %+v", rule)
	}
}

func TestHealthAndOverview(t *testing.T) {
	app := New(Config{Version: "test", Provider: "pve", NodeName: "node-a"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["node"] != "node-a" {
		t.Fatalf("node = %v", payload["node"])
	}

	resp, err = http.Get(server.URL + "/api/overview")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview status = %d", resp.StatusCode)
	}
}

func TestNATLifecycle(t *testing.T) {
	app := New(Config{Provider: "hyperv"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	body := `{"external_ip":"203.0.113.9","external_port":22000,"internal_ip":"10.0.0.8","internal_port":22,"protocol":"tcp"}`
	resp, err := http.Post(server.URL+"/api/nat", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	var created NATRule
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/nat/"+created.ID, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
}

func TestNATSyncReplacesRulesForExternalIP(t *testing.T) {
	app := New(Config{Provider: "pve"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	body := `{"external_ip":"203.0.113.88","mappings":[{"external_port":21000,"internal_ip":"10.0.0.20","internal_port":22,"protocol":"tcp","status":"active"}]}`
	resp, err := http.Post(server.URL+"/api/nat/sync", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sync status = %d", resp.StatusCode)
	}

	var syncResult struct {
		Removed int `json:"removed"`
		Added   int `json:"added"`
		Count   int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&syncResult); err != nil {
		t.Fatal(err)
	}
	if syncResult.Removed != 3 || syncResult.Added != 1 || syncResult.Count != 1 {
		t.Fatalf("unexpected sync receipt: %+v", syncResult)
	}

	resp, err = http.Get(server.URL + "/api/nat")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var list struct {
		Items []NATRule `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ExternalPort != 21000 {
		t.Fatalf("unexpected nat rules: %+v", list.Items)
	}
}

func TestTokenIsRequiredWhenConfigured(t *testing.T) {
	app := New(Config{Token: "secret"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/overview")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/overview", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized status = %d", resp.StatusCode)
	}
}

func TestNetworkPreviewAndApply(t *testing.T) {
	app := New(Config{NetworkBackend: "mock"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/network/preview?kind=nat")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d", resp.StatusCode)
	}

	var plan map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if plan["kind"] != "nat" {
		t.Fatalf("unexpected plan: %+v", plan)
	}

	resp, err = http.Post(server.URL+"/api/network/apply", "application/json", strings.NewReader(`{"kind":"nat","confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d", resp.StatusCode)
	}
}

func TestNetworkApplyRequiresConfirmation(t *testing.T) {
	app := New(Config{NetworkBackend: "mock"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/network/apply", "application/json", strings.NewReader(`{"kind":"nat","confirm":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
}

func TestNetworkApplyBlockedWithoutAllowFlagForRealBackend(t *testing.T) {
	app := New(Config{NetworkBackend: "nftables", WANInterface: "eth0", NATCIDR: "10.0.0.0/24"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/network/apply", "application/json", strings.NewReader(`{"kind":"nat","confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestNetworkVerifyEndpoint(t *testing.T) {
	app := New(Config{NetworkBackend: "mock"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/network/verify?kind=nat")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify status = %d", resp.StatusCode)
	}

	var payload struct {
		Backend  string `json:"backend"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Verified || payload.Backend != "mock" {
		t.Fatalf("unexpected verification: %+v", payload)
	}
}

func TestNetworkRepairAndRollback(t *testing.T) {
	app := New(Config{NetworkBackend: "mock"})
	engine := &driftEngine{}
	app.network = engine
	app.allowNetworkApply = true
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/network/repair", "application/json", strings.NewReader(`{"kind":"nat","confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repair status = %d", resp.StatusCode)
	}

	var repair struct {
		Status   string `json:"status"`
		BackupID string `json:"backup_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&repair); err != nil {
		t.Fatal(err)
	}
	if repair.Status != "repaired" || repair.BackupID == "" || engine.applied != 1 {
		t.Fatalf("unexpected repair: %+v applied=%d", repair, engine.applied)
	}

	resp, err = http.Post(server.URL+"/api/network/rollback", "application/json", strings.NewReader(`{"backup_id":"`+repair.BackupID+`","confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback status = %d", resp.StatusCode)
	}
	if engine.applied != 2 {
		t.Fatalf("expected rollback apply, got %d", engine.applied)
	}
}

func TestDiagnosticsEndpoint(t *testing.T) {
	app := New(Config{Provider: "pve", NodeID: "pve-hk-01"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diagnostics status = %d", resp.StatusCode)
	}

	var payload struct {
		Status string `json:"status"`
		NodeID string `json:"node_id"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.NodeID != "pve-hk-01" {
		t.Fatalf("unexpected node id: %s", payload.NodeID)
	}
	if len(payload.Checks) < 4 {
		t.Fatalf("expected checks, got %d", len(payload.Checks))
	}
	foundProvider := false
	for _, check := range payload.Checks {
		if check.Name == "provider" {
			foundProvider = true
			if check.Status != "ok" {
				t.Fatalf("provider check failed: %s", check.Status)
			}
		}
	}
	if !foundProvider {
		t.Fatal("provider check missing")
	}
}
