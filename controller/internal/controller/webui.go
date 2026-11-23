package controller

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed web/*
var webFS embed.FS

type Image struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	NodeID    string    `json:"node_id"`
	OSType    string    `json:"ostype"`
	Template  string    `json:"template"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}

type IPAllocationDetail struct {
	IP     string `json:"ip"`
	NodeID string `json:"node_id"`
	VMID   string `json:"vm_id"`
}

type IPPool struct {
	ID                string               `json:"id"`
	Name              string               `json:"name"`
	NodeID            string               `json:"node_id"`
	Region            string               `json:"region"`
	Type              string               `json:"type"`
	CIDR              string               `json:"cidr"`
	Gateway           string               `json:"gateway"`
	Start             string               `json:"start"`
	End               string               `json:"end"`
	Allocations       []string             `json:"allocations,omitempty"`
	AllocationDetails []IPAllocationDetail `json:"allocation_details,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
}

type persistedState struct {
	Nodes         []Node            `json:"nodes"`
	Images        []Image           `json:"images"`
	IPPools       []IPPool          `json:"ip_pools"`
	VMAllocations map[string]string `json:"vm_allocations,omitempty"`
}

func (c *Controller) LoadState(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statePath = path
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var state persistedState
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	c.images = state.Images
	c.ipPools = state.IPPools
	for index := range c.ipPools {
		if c.ipPools[index].Type == "" {
			c.ipPools[index].Type = "public"
		}
		if c.ipPools[index].Region == "" {
			if node, ok := c.nodes[c.ipPools[index].NodeID]; ok {
				c.ipPools[index].Region = node.Region
			}
		}
	}
	if state.VMAllocations != nil {
		c.vmAllocations = state.VMAllocations
	}
	for _, node := range state.Nodes {
		if node.ID == "" {
			continue
		}
		if _, exists := c.nodes[node.ID]; !exists {
			c.nodes[node.ID] = &nodeState{Node: node, Status: "offline"}
			c.order = append(c.order, node.ID)
		}
	}
	return nil
}

func (c *Controller) hostsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input Node
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	input.ID = strings.TrimSpace(input.ID)
	input.Name = strings.TrimSpace(input.Name)
	input.Provider = strings.TrimSpace(input.Provider)
	input.Region = strings.TrimSpace(input.Region)
	input.URL = strings.TrimSpace(input.URL)
	if input.ID == "" || input.Name == "" || input.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id, name and url are required"})
		return
	}
	if input.Provider == "" {
		input.Provider = "pve"
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.nodes[input.ID]; exists {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "host id already exists"})
		return
	}
	if input.IPPoolID != "" && c.findPool(input.IPPoolID) == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ip pool not found"})
		return
	}
	c.nodes[input.ID] = &nodeState{Node: input, Status: "offline"}
	c.order = append(c.order, input.ID)
	c.persistLocked()
	writeJSON(w, http.StatusCreated, input)
}

func (c *Controller) hostItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/hosts/")
	if id == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not found"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var input Node
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		existing, ok := c.nodes[id]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not found"})
			return
		}
		if input.IPPoolID != "" && c.findPool(input.IPPoolID) == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ip pool not found"})
			return
		}
		input.ID = id
		input.Fingerprint = existing.Fingerprint
		input.Token = firstNonEmpty(input.Token, existing.Token)
		existing.Node = input
		c.persistLocked()
		writeJSON(w, http.StatusOK, input)
	case http.MethodDelete:
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, exists := c.nodes[id]; !exists {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not found"})
			return
		}
		delete(c.nodes, id)
		for index, nodeID := range c.order {
			if nodeID == id {
				c.order = append(c.order[:index], c.order[index+1:]...)
				break
			}
		}
		c.persistLocked()
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (c *Controller) imagesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c.mu.RLock()
		items := append([]Image(nil), c.images...)
		c.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var input Image
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		input.NodeID = strings.TrimSpace(input.NodeID)
		if input.Name == "" || input.NodeID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and node_id are required"})
			return
		}
		input.ID = newID("img")
		input.CreatedAt = time.Now()
		c.mu.Lock()
		if _, ok := c.nodes[input.NodeID]; !ok {
			c.mu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node not found"})
			return
		}
		c.images = append(c.images, input)
		c.persistLocked()
		c.mu.Unlock()
		writeJSON(w, http.StatusCreated, input)
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (c *Controller) imageItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPut {
		w.Header().Set("Allow", "PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/images/")
	if r.Method == http.MethodPut {
		var input struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		for index := range c.images {
			if c.images[index].ID == id {
				c.images[index].Name = input.Name
				c.persistLocked()
				writeJSON(w, http.StatusOK, c.images[index])
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "image not found"})
		return
	}
	c.mu.RLock()
	var image Image
	found := false
	for _, item := range c.images {
		if item.ID == id {
			image = item
			found = true
			break
		}
	}
	var node *nodeState
	if found {
		node = c.nodes[image.NodeID]
	}
	c.mu.RUnlock()
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "image not found"})
		return
	}
	if node != nil && strings.Contains(image.Path, "/templates/") {
		status, err := c.deleteNodeTemplate(node, image.Path)
		if err != nil {
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for index, item := range c.images {
		if item.ID == id {
			c.images = append(c.images[:index], c.images[index+1:]...)
			c.persistLocked()
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "image not found"})
}

func (c *Controller) deleteNodeTemplate(node *nodeState, path string) (int, error) {
	payload, _ := json.Marshal(map[string]string{"path": path})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(node.URL, "/")+"/api/templates/delete", strings.NewReader(string(payload)))
	if err != nil {
		return http.StatusBadGateway, err
	}
	req.Header.Set("Content-Type", "application/json")
	if node.Token != "" {
		req.Header.Set("Authorization", "Bearer "+node.Token)
	}
	client := &http.Client{Transport: c.client.Transport, Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("删除模板文件失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &payload)
		message := strings.TrimSpace(payload.Error)
		if message == "" {
			message = strings.TrimSpace(string(body))
		}
		if message == "" {
			message = "删除模板文件失败"
		}
		if resp.StatusCode == http.StatusConflict {
			return http.StatusConflict, fmt.Errorf("%s", message)
		}
		return http.StatusBadGateway, fmt.Errorf("%s", message)
	}
	return http.StatusOK, nil
}

func (c *Controller) ipPoolsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c.mu.RLock()
		items := append([]IPPool(nil), c.ipPools...)
		for index := range items {
			items[index].AllocationDetails = nil
			for key, allocation := range c.vmAllocations {
				parts := strings.SplitN(allocation, "|", 2)
				if len(parts) != 2 || parts[0] != items[index].ID {
					continue
				}
				keyParts := strings.SplitN(key, "/", 2)
				nodeID := ""
				vmID := ""
				if len(keyParts) == 2 {
					nodeID = keyParts[0]
					vmID = keyParts[1]
				}
				items[index].AllocationDetails = append(items[index].AllocationDetails, IPAllocationDetail{IP: parts[1], NodeID: nodeID, VMID: vmID})
			}
		}
		c.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var input IPPool
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		input.NodeID = strings.TrimSpace(input.NodeID)
		input.CIDR = strings.TrimSpace(input.CIDR)
		input.Type = strings.TrimSpace(input.Type)
		input.Region = strings.TrimSpace(input.Region)
		if input.Type == "" {
			input.Type = "public"
		}
		if input.Type != "public" && input.Type != "private" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be public or private"})
			return
		}
		if input.Name == "" || input.CIDR == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and cidr are required"})
			return
		}
		input.ID = newID("ip")
		input.CreatedAt = time.Now()
		c.mu.Lock()
		if input.NodeID != "" {
			if _, ok := c.nodes[input.NodeID]; !ok {
				c.mu.Unlock()
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node not found"})
				return
			}
			if input.Region == "" {
				input.Region = c.nodes[input.NodeID].Region
			}
		}
		if input.Type == "private" && input.Region == "" {
			input.Region = "global"
		}
		if input.Region == "" {
			c.mu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "region is required when pool is not bound to a node"})
			return
		}
		c.ipPools = append(c.ipPools, input)
		c.persistLocked()
		c.mu.Unlock()
		writeJSON(w, http.StatusCreated, input)
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (c *Controller) ipPoolItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/ip-pools/")
	if id == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ip pool not found"})
		return
	}
	switch r.Method {
	case http.MethodDelete:
		c.mu.Lock()
		defer c.mu.Unlock()
		for index, pool := range c.ipPools {
			if pool.ID == id {
				c.ipPools = append(c.ipPools[:index], c.ipPools[index+1:]...)
				c.persistLocked()
				writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ip pool not found"})
	case http.MethodPut:
		var input IPPool
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		input.NodeID = strings.TrimSpace(input.NodeID)
		input.CIDR = strings.TrimSpace(input.CIDR)
		input.Type = strings.TrimSpace(input.Type)
		input.Region = strings.TrimSpace(input.Region)
		input.Gateway = strings.TrimSpace(input.Gateway)
		input.Start = strings.TrimSpace(input.Start)
		input.End = strings.TrimSpace(input.End)

		c.mu.Lock()
		defer c.mu.Unlock()
		index := -1
		for i := range c.ipPools {
			if c.ipPools[i].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ip pool not found"})
			return
		}
		existing := c.ipPools[index]
		if input.Type == "" {
			input.Type = existing.Type
		}
		if input.Type != "public" && input.Type != "private" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be public or private"})
			return
		}
		if input.Name == "" || input.CIDR == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and cidr are required"})
			return
		}
		if input.NodeID != "" {
			node, ok := c.nodes[input.NodeID]
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node not found"})
				return
			}
			if input.Region == "" {
				input.Region = node.Region
			}
		}
		if input.Type == "private" && input.Region == "" {
			input.Region = "global"
		}
		if input.Region == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "region is required when pool is not bound to a node"})
			return
		}
		_, network, err := net.ParseCIDR(input.CIDR)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid cidr"})
			return
		}
		for _, raw := range []string{input.Gateway, input.Start, input.End} {
			if raw == "" {
				continue
			}
			ip := net.ParseIP(raw)
			if ip == nil || !network.Contains(ip) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "gateway/start/end must be inside cidr"})
				return
			}
		}
		if input.Start != "" && input.End != "" && bytes.Compare(net.ParseIP(input.Start).To16(), net.ParseIP(input.End).To16()) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "start must not be greater than end"})
			return
		}
		for _, allocated := range existing.Allocations {
			if input.CIDR != existing.CIDR && len(existing.Allocations) > 0 {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "cannot change cidr while addresses are allocated"})
				return
			}
			ip := net.ParseIP(allocated)
			if ip == nil || !network.Contains(ip) || !withinPoolRange(ip, input.Start, input.End) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "an allocated address would be outside the new range"})
				return
			}
		}
		input.ID = id
		input.CreatedAt = existing.CreatedAt
		input.Allocations = existing.Allocations
		c.ipPools[index] = input
		c.persistLocked()
		writeJSON(w, http.StatusOK, input)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func withinPoolRange(ip net.IP, start, end string) bool {
	if start == "" && end == "" {
		return true
	}
	if start != "" && bytes.Compare(ip.To16(), net.ParseIP(start).To16()) < 0 {
		return false
	}
	if end != "" && bytes.Compare(ip.To16(), net.ParseIP(end).To16()) > 0 {
		return false
	}

	return true
}

func (c *Controller) persistLocked() {
	if c.statePath == "" {
		return
	}
	nodes := make([]Node, 0, len(c.order))
	for _, id := range c.order {
		if node, ok := c.nodes[id]; ok {
			nodes = append(nodes, node.Node)
		}
	}
	raw, err := json.MarshalIndent(persistedState{
		Nodes:         nodes,
		Images:        c.images,
		IPPools:       c.ipPools,
		VMAllocations: c.vmAllocations,
	}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(c.statePath, raw, 0o600)
}

func newID(prefix string) string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(buf)
}

func (c *Controller) webHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(sub))
	appPages := map[string]bool{
		"/overview":   true,
		"/hosts":      true,
		"/vms":        true,
		"/images":     true,
		"/pools":      true,
		"/api-status": true,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/overview", http.StatusSeeOther)
			return
		}
		if appPages[r.URL.Path] || strings.HasPrefix(r.URL.Path, "/vm/") {
			c.writeIndex(w)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (c *Controller) writeIndex(w http.ResponseWriter) {
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "controller UI unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
