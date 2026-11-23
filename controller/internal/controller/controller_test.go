package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNodeListAndProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/overview":
			_, _ = w.Write([]byte(`{"node":"upstream"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	app := New([]Node{{ID: "node-1", Name: "Node 1", Provider: "pve", URL: upstream.URL}})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/nodes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("nodes status = %d", resp.StatusCode)
	}

	var list struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Status != "online" {
		t.Fatalf("unexpected list: %+v", list)
	}

	resp, err = http.Get(server.URL + "/api/nodes/node-1/overview")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d", resp.StatusCode)
	}
}

func TestParseNodesDefault(t *testing.T) {
	nodes, err := ParseNodes("")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("nodes = %d", len(nodes))
	}
}

func TestParseNodesInvalid(t *testing.T) {
	if _, err := ParseNodes("{"); err == nil {
		t.Fatal("expected error")
	}
}

func TestAgentRegistrationRequiresTLSClientCertificate(t *testing.T) {
	app := NewWithOptions(nil, nil, true, "")
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/agent/register", "application/json", strings.NewReader(`{"node_id":"n1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
