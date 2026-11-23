package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebUIAndImagePoolRegistry(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	app := New([]Node{{ID: "node-1", Name: "Node 1", Provider: "pve", Region: "香港"}})
	if err := app.LoadState(statePath); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("web root status = %d", resp.StatusCode)
	}
	body := make([]byte, 256)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "Deviy Controller") {
		t.Fatalf("unexpected web root: %q", string(body[:n]))
	}

	image := `{"name":"Debian 12","node_id":"node-1","ostype":"l26","template":"9000"}`
	resp, err = http.Post(server.URL+"/api/images", "application/json", strings.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create image status = %d", resp.StatusCode)
	}

	pool := `{"name":"HK-IP","node_id":"node-1","cidr":"203.0.113.0/24"}`
	resp, err = http.Post(server.URL+"/api/ip-pools", "application/json", strings.NewReader(pool))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create pool status = %d", resp.StatusCode)
	}

	app.mu.RLock()
	poolID := app.ipPools[0].ID
	app.mu.RUnlock()
	poolUpdate := `{"name":"HK-IP-Updated","node_id":"node-1","cidr":"203.0.113.0/24","gateway":"203.0.113.1","start":"203.0.113.10","end":"203.0.113.200"}`
	req, err := http.NewRequest(http.MethodPut, server.URL+"/api/ip-pools/"+poolID, strings.NewReader(poolUpdate))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update pool status = %d", resp.StatusCode)
	}
	app.mu.RLock()
	updatedPool := app.ipPools[0]
	app.mu.RUnlock()
	if updatedPool.Name != "HK-IP-Updated" || updatedPool.End != "203.0.113.200" {
		t.Fatalf("pool not updated: %+v", updatedPool)
	}

	host := `{"id":"node-2","name":"Node 2","provider":"hyperv","region":"US","url":"http://127.0.0.1:8444"}`
	resp, err = http.Post(server.URL+"/api/hosts", "application/json", strings.NewReader(host))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host status = %d", resp.StatusCode)
	}
	app.mu.RLock()
	_, hostExists := app.nodes["node-2"]
	app.mu.RUnlock()
	if !hostExists {
		t.Fatal("host was not added")
	}

	update := `{"name":"Node 2 Updated","provider":"hyperv","region":"US","url":"http://127.0.0.1:8444","public_bridge":"vmbr9","private_bridge":"vmbr8"}`
	req, err = http.NewRequest(http.MethodPut, server.URL+"/api/hosts/node-2", strings.NewReader(update))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update host status = %d", resp.StatusCode)
	}
	app.mu.RLock()
	updated := app.nodes["node-2"]
	app.mu.RUnlock()
	if updated.Name != "Node 2 Updated" || updated.PublicBridge != "vmbr9" {
		t.Fatalf("host not updated: %+v", updated.Node)
	}

	resp, err = http.Get(server.URL + "/api/images")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var images struct {
		Items []Image `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&images); err != nil {
		t.Fatal(err)
	}
	if len(images.Items) != 1 || images.Items[0].Name != "Debian 12" {
		t.Fatalf("unexpected images: %+v", images.Items)
	}

	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file not written: %v", err)
	}
}
