package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVMLifecycle(t *testing.T) {
	app := New(Config{Provider: "pve", Mode: "mock"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/vms")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}

	var list struct {
		Items []struct {
			VMID int `json:"vmid"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("expected 3 seed vms, got %d", len(list.Items))
	}

	body := `{"name":"unit-vm","cores":2,"memory_mb":2048,"disk_gb":20,"start":true}`
	resp, err = http.Post(server.URL+"/api/vms", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	var created struct {
		ID       string `json:"id"`
		VMID     int    `json:"vmid"`
		MemoryMB int    `json:"memory_mb"`
		DiskGB   int    `json:"disk_gb"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.MemoryMB != 2048 || created.DiskGB != 20 {
		t.Fatalf("create ignored custom spec: %+v", created)
	}
	if created.ID == "" {
		t.Fatal("create did not return id")
	}

	resp, err = http.Post(server.URL+"/api/vms/"+created.ID+"/action", "application/json", strings.NewReader(`{"action":"stop"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("action status = %d", resp.StatusCode)
	}
}
