package heartbeat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestRegisterAndHeartbeat(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	payloads := []map[string]any{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		payloads = append(payloads, payload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client, err := New(Config{
		ControllerURL: server.URL,
		NodeID:        "pve-hk-01",
		Name:          "PVE-HK-01",
		Provider:      "pve",
		Region:        "香港",
		Version:       "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}

	client.register(context.Background())
	client.heartbeat(context.Background(), func() map[string]any {
		return map[string]any{"status": "online", "vms_total": 3, "vms_running": 2}
	})

	if len(paths) != 2 || paths[0] != "/api/agent/register" || paths[1] != "/api/agent/heartbeat" {
		t.Fatalf("unexpected paths: %+v", paths)
	}
	if payloads[0]["node_id"] != "pve-hk-01" {
		t.Fatalf("register payload: %+v", payloads[0])
	}
	if payloads[1]["vms_running"] != float64(2) {
		t.Fatalf("heartbeat payload: %+v", payloads[1])
	}
}

func TestNewRequiresNodeID(t *testing.T) {
	if _, err := New(Config{ControllerURL: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("expected node id error")
	}
}

func TestNewWithoutControllerIsDisabled(t *testing.T) {
	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if client != nil {
		t.Fatal("expected nil client when controller URL is empty")
	}
}
