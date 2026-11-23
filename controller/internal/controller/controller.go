package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Node struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Region      string `json:"region"`
	URL         string `json:"url"`
	Token       string `json:"token,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`

	NodeIP        string `json:"node_ip,omitempty"`
	IPPoolID      string `json:"ip_pool_id,omitempty"`
	PublicBridge  string `json:"public_bridge,omitempty"`
	PrivateBridge string `json:"private_bridge,omitempty"`

	SystemDiskMinMBps int    `json:"system_disk_min_mbps,omitempty"`
	SystemDiskMaxMBps int    `json:"system_disk_max_mbps,omitempty"`
	DataDiskMinMBps   int    `json:"data_disk_min_mbps,omitempty"`
	DataDiskMaxMBps   int    `json:"data_disk_max_mbps,omitempty"`
	TemplateDir       string `json:"template_dir,omitempty"`
	SystemDiskDir     string `json:"system_disk_dir,omitempty"`
	DataDiskDir       string `json:"data_disk_dir,omitempty"`
	ISODir            string `json:"iso_dir,omitempty"`
	BackupDir         string `json:"backup_dir,omitempty"`
	BackupStorage     string `json:"backup_storage,omitempty"`
	DNS1              string `json:"dns1,omitempty"`
	DNS2              string `json:"dns2,omitempty"`
	PortMin           int    `json:"port_min,omitempty"`
	PortMax           int    `json:"port_max,omitempty"`
	ExcludePorts      string `json:"exclude_ports,omitempty"`
}

type nodeState struct {
	Node
	Status          string    `json:"status"`
	LastSeen        time.Time `json:"last_seen"`
	CertExpiresAt   time.Time `json:"cert_expires_at"`
	CertFingerprint string    `json:"cert_fingerprint"`
	Version         string    `json:"agent_version"`
	VMsTotal        int       `json:"vms_total"`
	VMsRunning      int       `json:"vms_running"`
}

type Controller struct {
	mu            sync.RWMutex
	nodes         map[string]*nodeState
	order         []string
	client        *http.Client
	allowRegister bool
	registerToken string
	statePath     string
	images        []Image
	ipPools       []IPPool
	vmAllocations map[string]string
	authMu        sync.Mutex
	adminUser     string
	adminPassword string
	apiToken      string
	sessions      map[string]time.Time
	loginFailures map[string]loginFailure
}

func New(nodes []Node) *Controller {
	return NewWithOptions(nodes, &http.Client{Timeout: 12 * time.Second}, false, "")
}

func NewWithClient(nodes []Node, client *http.Client) *Controller {
	return NewWithOptions(nodes, client, false, "")
}

func NewWithOptions(nodes []Node, client *http.Client, allowRegister bool, registerToken string) *Controller {
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}

	controller := &Controller{
		nodes:         map[string]*nodeState{},
		client:        client,
		allowRegister: allowRegister,
		registerToken: registerToken,
		vmAllocations: map[string]string{},
	}
	for _, node := range nodes {
		controller.nodes[node.ID] = &nodeState{Node: node, Status: "offline"}
		controller.order = append(controller.order, node.ID)
	}

	return controller
}

func (c *Controller) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", c.health)
	mux.HandleFunc("/login", c.loginPage)
	mux.HandleFunc("/api/login", c.login)
	mux.HandleFunc("/logout", c.logout)
	mux.HandleFunc("/api/nodes", c.listNodes)
	mux.HandleFunc("/api/nodes/", c.nodeProxy)
	mux.HandleFunc("/api/agent/register", c.register)
	mux.HandleFunc("/api/agent/heartbeat", c.heartbeat)
	mux.HandleFunc("/api/images", c.imagesHandler)
	mux.HandleFunc("/api/images/", c.imageItem)
	mux.HandleFunc("/api/ip-pools", c.ipPoolsHandler)
	mux.HandleFunc("/api/ip-pools/", c.ipPoolItem)
	mux.HandleFunc("/api/hosts", c.hostsHandler)
	mux.HandleFunc("/api/hosts/", c.hostItem)
	mux.Handle("/", c.webHandler())

	return c.withRequestLog(c.authMiddleware(mux))
}

func (c *Controller) health(w http.ResponseWriter, _ *http.Request) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"role":   "controller",
		"nodes":  len(c.nodes),
	})
}

func (c *Controller) listNodes(w http.ResponseWriter, _ *http.Request) {
	c.mu.RLock()
	states := make([]nodeState, 0, len(c.order))
	for _, id := range c.order {
		if node, ok := c.nodes[id]; ok {
			states = append(states, *node)
		}
	}
	c.mu.RUnlock()

	for i := range states {
		state := &states[i]
		if time.Since(state.LastSeen) <= 60*time.Second && !state.LastSeen.IsZero() {
			state.Status = "online"

			continue
		}
		if state.URL != "" && probeHealth(c.client, state.URL, state.Token) {
			state.Status = "online"
		} else {
			state.Status = "offline"
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": states})
}

func (c *Controller) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)

		return
	}

	cert, err := peerCertificate(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})

		return
	}
	if c.registerToken != "" && r.Header.Get("X-Deviy-Register-Token") != c.registerToken {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid register token"})

		return
	}

	var input struct {
		NodeID   string `json:"node_id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Region   string `json:"region"`
		URL      string `json:"url"`
		Version  string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.NodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})

		return
	}

	fingerprint := certificateFingerprint(cert)

	c.mu.Lock()
	defer c.mu.Unlock()

	state, exists := c.nodes[input.NodeID]
	if !exists && !c.allowRegister {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "node registration is disabled"})

		return
	}
	if exists && state.Fingerprint != "" && state.Fingerprint != fingerprint {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "certificate fingerprint mismatch"})

		return
	}
	if !exists {
		state = &nodeState{Node: Node{ID: input.NodeID, Name: input.Name, Provider: input.Provider, Region: input.Region, URL: input.URL, Fingerprint: fingerprint}}
		c.nodes[input.NodeID] = state
		c.order = append(c.order, input.NodeID)
	}

	state.Name = firstNonEmpty(input.Name, state.Name)
	state.Provider = firstNonEmpty(input.Provider, state.Provider)
	state.Region = firstNonEmpty(input.Region, state.Region)
	state.URL = firstNonEmpty(input.URL, state.URL)
	state.Fingerprint = fingerprint
	state.CertFingerprint = fingerprint
	state.CertExpiresAt = cert.Leaf.NotAfter
	state.Version = input.Version
	state.LastSeen = time.Now()
	state.Status = "online"

	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "registered",
		"node_id":       state.ID,
		"fingerprint":   fingerprint,
		"cert_expires":  state.CertExpiresAt.Format(time.RFC3339),
		"heartbeat_url": "/api/agent/heartbeat",
	})
}

func (c *Controller) heartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)

		return
	}

	cert, err := peerCertificate(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})

		return
	}

	var input struct {
		NodeID   string `json:"node_id"`
		Status   string `json:"status"`
		Version  string `json:"version"`
		Provider string `json:"provider"`
		Region   string `json:"region"`
		VMsTotal int    `json:"vms_total"`
		VMsRun   int    `json:"vms_running"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.NodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})

		return
	}

	fingerprint := certificateFingerprint(cert)

	c.mu.Lock()
	defer c.mu.Unlock()

	state, ok := c.nodes[input.NodeID]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node is not registered"})

		return
	}
	if state.Fingerprint != "" && state.Fingerprint != fingerprint {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "certificate fingerprint mismatch"})

		return
	}

	state.Fingerprint = fingerprint
	state.CertFingerprint = fingerprint
	state.CertExpiresAt = cert.Leaf.NotAfter
	state.LastSeen = time.Now()
	state.Status = "online"
	state.Version = input.Version
	state.Provider = firstNonEmpty(input.Provider, state.Provider)
	state.Region = firstNonEmpty(input.Region, state.Region)
	state.VMsTotal = input.VMsTotal
	state.VMsRunning = input.VMsRun

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "next_heartbeat_seconds": 15})
}

func (c *Controller) nodeProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/nodes/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not found"})

		return
	}

	c.mu.RLock()
	node, ok := c.nodes[parts[0]]
	c.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not found"})

		return
	}

	suffix := ""
	if len(parts) == 2 {
		suffix = "/" + parts[1]
	}
	if strings.HasSuffix(suffix, "/template") && r.Method == http.MethodPost {
		c.nodeTemplate(w, r, node, suffix)
		return
	}
	if strings.HasPrefix(suffix, "/upload/") && r.Method == http.MethodPost {
		c.nodeUpload(w, r, node, suffix)
		return
	}
	if strings.HasSuffix(suffix, "/vnc-ticket") && r.Method == http.MethodPost {
		c.vncTicket(w, node, suffix)
		return
	}
	if strings.HasSuffix(suffix, "/vncws") && r.Method == http.MethodGet {
		c.vncWebSocketProxy(w, r, node, suffix)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})

		return
	}

	var allocatedPool, allocatedIP string
	var natAllocation *natAllocationRequest
	if r.Method == http.MethodPost && suffix == "/vms" {
		body, allocatedPool, allocatedIP, err = c.prepareVMCreate(body, node)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})

			return
		}
		if allocatedIP != "" {
			natAllocation = buildNATAllocation(node, body, allocatedIP)
		}
	}

	target := strings.TrimRight(node.URL, "/") + "/api/" + strings.TrimPrefix(suffix, "/")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, strings.NewReader(string(body)))
	if err != nil {
		c.releaseAllocated(allocatedPool, allocatedIP)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid upstream request"})

		return
	}
	req.Header.Set("Content-Type", "application/json")
	if node.Token != "" {
		req.Header.Set("Authorization", "Bearer "+node.Token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.releaseAllocated(allocatedPool, allocatedIP)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":   "upstream unavailable",
			"node":    node.Name,
			"details": err.Error(),
		})

		return
	}
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		c.releaseAllocated(allocatedPool, allocatedIP)
	} else {
		c.rememberVMCreate(node.ID, suffix, responseBody, allocatedPool, allocatedIP)
		if natAllocation != nil {
			c.allocateNAT(node, *natAllocation)
			go c.reconcileNAT(node, responseBody, *natAllocation)
		}
		go c.provisionVMCreate(node, responseBody, body, allocatedIP)
		c.releaseDeletedVM(node.ID, suffix, r.Method, body, resp.StatusCode)
		if r.Method == http.MethodGet && suffix == "/vms" {
			c.updateVMCounts(node.ID, responseBody)
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Deviy-Node", node.Name)
	if allocatedIP != "" {
		w.Header().Set("X-Deviy-IP", allocatedIP)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(responseBody)
}

func (c *Controller) nodeTemplate(w http.ResponseWriter, r *http.Request, node *nodeState, suffix string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	target := strings.TrimRight(node.URL, "/") + "/api" + suffix
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid upstream request"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if node.Token != "" {
		req.Header.Set("Authorization", "Bearer "+node.Token)
	}
	client := &http.Client{Transport: c.client.Transport}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "模板转换请求失败", "details": err.Error()})
		return
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 300 {
		var info struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			OSType string `json:"ostype"`
		}
		if err := json.Unmarshal(responseBody, &info); err == nil && strings.TrimSpace(info.Path) != "" {
			c.mu.Lock()
			found := false
			for index := range c.images {
				if c.images[index].Path == info.Path {
					if strings.TrimSpace(info.Name) != "" {
						c.images[index].Name = info.Name
					}
					if strings.TrimSpace(info.OSType) != "" {
						c.images[index].OSType = info.OSType
					}
					found = true
					break
				}
			}
			if !found {
				name := strings.TrimSpace(info.Name)
				if name == "" {
					name = strings.TrimSuffix(filepath.Base(info.Path), filepath.Ext(info.Path))
				}
				c.images = append(c.images, Image{ID: newID("img"), Name: name, NodeID: node.ID, OSType: info.OSType, Path: info.Path, CreatedAt: time.Now()})
			}
			c.persistLocked()
			c.mu.Unlock()
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Deviy-Node", node.Name)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(responseBody)
}
func (c *Controller) nodeUpload(w http.ResponseWriter, r *http.Request, node *nodeState, suffix string) {
	kind := strings.TrimPrefix(suffix, "/upload/")
	if kind != "iso" && kind != "template" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown upload kind"})
		return
	}
	filename := strings.TrimSpace(r.URL.Query().Get("filename"))
	if filename == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "filename required"})
		return
	}
	target := strings.TrimRight(node.URL, "/") + "/api/upload/" + kind + "?filename=" + url.QueryEscape(filename)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid upstream request"})
		return
	}
	req.ContentLength = r.ContentLength
	req.Header.Set("Content-Type", "application/octet-stream")
	if node.Token != "" {
		req.Header.Set("Authorization", "Bearer "+node.Token)
	}
	uploadClient := &http.Client{Transport: c.client.Transport}
	resp, err := uploadClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "node": node.Name})
		return
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 300 {
		var info struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			OSType string `json:"ostype"`
		}
		if err := json.Unmarshal(responseBody, &info); err == nil && strings.TrimSpace(info.Path) != "" {
			c.mu.Lock()
			found := false
			for index := range c.images {
				if c.images[index].Path == info.Path {
					if strings.TrimSpace(info.Name) != "" {
						c.images[index].Name = info.Name
					}
					if strings.TrimSpace(info.OSType) != "" {
						c.images[index].OSType = info.OSType
					}
					found = true
					break
				}
			}
			if !found {
				name := strings.TrimSpace(info.Name)
				if name == "" {
					name = strings.TrimSuffix(filepath.Base(info.Path), filepath.Ext(info.Path))
				}
				c.images = append(c.images, Image{ID: newID("img"), Name: name, NodeID: node.ID, OSType: info.OSType, Path: info.Path, CreatedAt: time.Now()})
			}
			c.persistLocked()
			c.mu.Unlock()
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Deviy-Node", node.Name)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(responseBody)
}
func (c *Controller) vncTicket(w http.ResponseWriter, node *nodeState, suffix string) {
	vmID := strings.TrimSuffix(strings.TrimPrefix(suffix, "/vms/"), "/vnc-ticket")
	if vmID == "" || strings.Contains(vmID, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid vm id"})
		return
	}

	expires := time.Now().Add(5 * time.Minute).Unix()
	ticket := c.signVNCTicket(node.ID, vmID, expires)
	path := "/api/nodes/" + url.PathEscape(node.ID) + "/vms/" + url.PathEscape(vmID) + "/vncws?ticket=" + url.QueryEscape(ticket)
	noVNC := "/novnc/vnc.html?autoconnect=1&resize=scale&reconnect=1&path=" + url.QueryEscape(path)
	writeJSON(w, http.StatusOK, map[string]any{"url": noVNC, "expires_at": expires})
}

func (c *Controller) signVNCTicket(nodeID, vmID string, expires int64) string {
	c.authMu.Lock()
	secret := c.apiToken
	if secret == "" {
		secret = c.adminPassword
	}
	c.authMu.Unlock()

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%s|%s|%d", nodeID, vmID, expires)
	return strconv.FormatInt(expires, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}

func (c *Controller) validVNCTicket(path, ticket string) bool {
	parts := strings.SplitN(ticket, ".", 2)
	if len(parts) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false
	}

	rest := strings.TrimPrefix(path, "/api/nodes/")
	segments := strings.SplitN(rest, "/vms/", 2)
	if len(segments) != 2 {
		return false
	}
	vmID := strings.TrimSuffix(segments[1], "/vncws")
	expected := c.signVNCTicket(segments[0], vmID, expires)
	return hmac.Equal([]byte(expected), []byte(ticket))
}
func (c *Controller) vncWebSocketProxy(w http.ResponseWriter, r *http.Request, node *nodeState, suffix string) {
	target, err := url.Parse(strings.TrimRight(node.URL, "/") + "/api" + suffix)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid agent URL"})
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	if c.client.Transport != nil {
		proxy.Transport = c.client.Transport
	}
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = target.Path
		req.URL.RawQuery = r.URL.RawQuery
		req.Host = target.Host
		req.Header.Set("Authorization", "Bearer "+node.Token)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": proxyErr.Error()})
	}
	proxy.ServeHTTP(w, r)
}

func (c *Controller) prepareVMCreate(body []byte, node *nodeState) ([]byte, string, string, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, "", "", nil
	}
	networkMode, _ := payload["network_mode"].(string)
	bridge := node.PublicBridge
	if networkMode == "private" {
		bridge = node.PrivateBridge
	}
	if bridge != "" {
		payload["bridge"] = bridge
	}
	if node.Provider == "hyperv" && node.SystemDiskDir != "" {
		payload["storage"] = node.SystemDiskDir
	}
	payload["system_disk_min_mbps"] = node.SystemDiskMinMBps
	payload["system_disk_max_mbps"] = node.SystemDiskMaxMBps
	payload["data_disk_min_mbps"] = node.DataDiskMinMBps
	payload["data_disk_max_mbps"] = node.DataDiskMaxMBps
	payload["template_dir"] = node.TemplateDir
	payload["system_disk_dir"] = node.SystemDiskDir
	payload["data_disk_dir"] = node.DataDiskDir
	payload["iso_dir"] = node.ISODir
	payload["backup_dir"] = node.BackupDir
	payload["dns1"] = node.DNS1
	payload["dns2"] = node.DNS2
	payload["port_min"] = node.PortMin
	payload["port_max"] = node.PortMax
	payload["exclude_ports"] = node.ExcludePorts
	poolID, _ := payload["ip_pool_id"].(string)
	if poolID == "" {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return body, "", "", nil
		}
		return encoded, "", "", nil
	}
	requested, _ := payload["ip"].(string)

	c.mu.Lock()
	defer c.mu.Unlock()
	ip, gateway, prefix, err := c.allocateIPLocked(poolID, requested)
	if err != nil {
		return body, "", "", err
	}
	payload["ip"] = ip
	payload["gateway"] = gateway
	payload["prefix_len"] = prefix
	encoded, err := json.Marshal(payload)
	if err != nil {
		c.releaseAllocationLocked(poolID, ip)
		return body, "", "", err
	}

	return encoded, poolID, ip, nil
}

func (c *Controller) rememberVMCreate(nodeID, suffix string, response []byte, poolID, ip string) {
	if poolID == "" || ip == "" || suffix != "/vms" {
		return
	}
	var vm struct {
		ID   string `json:"id"`
		VMID int    `json:"vmid"`
	}
	if err := json.Unmarshal(response, &vm); err != nil {
		return
	}
	vmID := strings.TrimSpace(vm.ID)
	if vmID == "" && vm.VMID > 0 {
		vmID = fmt.Sprintf("%d", vm.VMID)
	}
	if vmID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vmAllocations[vmAllocationKey(nodeID, vmID)] = poolID + "|" + ip
	c.persistLocked()
}

func (c *Controller) releaseDeletedVM(nodeID, suffix, method string, requestBody []byte, status int) {
	if method != http.MethodPost || !strings.HasSuffix(suffix, "/action") || status >= 300 {
		return
	}
	var payload struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(requestBody, &payload); err != nil || payload.Action != "delete" {
		return
	}
	vmID := strings.TrimPrefix(suffix, "/vms/")
	vmID = strings.TrimSuffix(vmID, "/action")
	if vmID == "" {
		return
	}
	key := vmAllocationKey(nodeID, vmID)
	c.mu.Lock()
	allocation, ok := c.vmAllocations[key]
	var releasedIP string
	if ok {
		parts := strings.SplitN(allocation, "|", 2)
		if len(parts) == 2 {
			c.releaseAllocationLocked(parts[0], parts[1])
			releasedIP = parts[1]
		}
		delete(c.vmAllocations, key)
		c.persistLocked()
	}
	c.mu.Unlock()
	if releasedIP == "" {
		return
	}
	c.mu.RLock()
	node := c.nodes[nodeID]
	c.mu.RUnlock()
	if node != nil {
		go c.deleteNATByInternalIP(node, releasedIP)
	}
}

func (c *Controller) deleteNATByInternalIP(node *nodeState, internalIP string) {
	target := strings.TrimRight(node.URL, "/") + "/api/nat"
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return
	}
	if node.Token != "" {
		req.Header.Set("Authorization", "Bearer "+node.Token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var payload struct {
		Items []struct {
			ID         string `json:"id"`
			InternalIP string `json:"internal_ip"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return
	}
	for _, item := range payload.Items {
		if item.InternalIP != internalIP || item.ID == "" {
			continue
		}
		deleteReq, err := http.NewRequest(http.MethodDelete, strings.TrimRight(node.URL, "/")+"/api/nat/"+url.PathEscape(item.ID), nil)
		if err != nil {
			continue
		}
		if node.Token != "" {
			deleteReq.Header.Set("Authorization", "Bearer "+node.Token)
		}
		if resp, err := c.client.Do(deleteReq); err == nil {
			_ = resp.Body.Close()
		}
	}
}

func (c *Controller) updateVMCounts(nodeID string, response []byte) {
	var payload struct {
		Items []struct {
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response, &payload); err != nil {
		return
	}
	running := 0
	for _, item := range payload.Items {
		if item.Status == "running" {
			running++
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if node, ok := c.nodes[nodeID]; ok {
		node.VMsTotal = len(payload.Items)
		node.VMsRunning = running
		c.persistLocked()
	}
}
func (c *Controller) releaseAllocated(poolID, ip string) {
	if poolID == "" || ip == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releaseAllocationLocked(poolID, ip)
}

type natAllocationRequest struct {
	ExternalIP   string `json:"external_ip"`
	InternalIP   string `json:"internal_ip"`
	InternalPort int    `json:"internal_port"`
	PortMin      int    `json:"port_min"`
	PortMax      int    `json:"port_max"`
	ExcludePorts string `json:"exclude_ports"`
}

func buildNATAllocation(node *nodeState, body []byte, internalIP string) *natAllocationRequest {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	mode, _ := payload["network_mode"].(string)
	if mode != "private" {
		return nil
	}
	externalIP, err := netip.ParseAddr(strings.TrimSpace(node.NodeIP))
	if err != nil || !externalIP.Is4() {
		return nil
	}
	portMin, portMax := node.PortMin, node.PortMax
	if portMin <= 0 {
		portMin = 10000
	}
	if portMax <= 0 {
		portMax = 30000
	}
	if portMax < portMin {
		return nil
	}
	internalPort := 22
	if ostype, _ := payload["ostype"].(string); strings.HasPrefix(strings.ToLower(ostype), "win") {
		internalPort = 3389
	}

	return &natAllocationRequest{
		ExternalIP:   externalIP.String(),
		InternalIP:   internalIP,
		InternalPort: internalPort,
		PortMin:      portMin,
		PortMax:      portMax,
		ExcludePorts: node.ExcludePorts,
	}
}

func (c *Controller) provisionVMCreate(node *nodeState, response, requestBody []byte, allocatedIP string) {
	var request struct {
		Password    string `json:"password"`
		OSType      string `json:"ostype"`
		Start       bool   `json:"start"`
		IP          string `json:"ip"`
		Gateway     string `json:"gateway"`
		PrefixLen   int    `json:"prefix_len"`
		NetworkMode string `json:"network_mode"`
		DataDiskGB  int    `json:"data_disk_gb"`
		DiskGB      int    `json:"disk_gb"`
		DNS1        string `json:"dns1"`
		DNS2        string `json:"dns2"`
	}
	if err := json.Unmarshal(requestBody, &request); err != nil || request.Password == "" || !request.Start {
		return
	}
	var vm struct {
		ID   string `json:"id"`
		VMID int    `json:"vmid"`
	}
	if err := json.Unmarshal(response, &vm); err != nil {
		return
	}
	vmID := strings.TrimSpace(vm.ID)
	if vmID == "" && vm.VMID > 0 {
		vmID = fmt.Sprintf("%d", vm.VMID)
	}
	if vmID == "" {
		return
	}
	username := "root"
	if strings.HasPrefix(strings.ToLower(request.OSType), "win") {
		username = "Administrator"
	}
	client := &http.Client{Timeout: 35 * time.Second}
	for attempt := 0; attempt < 30; attempt++ {
		if attempt > 0 {
			time.Sleep(10 * time.Second)
		}
		guestIP := allocatedIP
		req, err := http.NewRequest(http.MethodGet, strings.TrimRight(node.URL, "/")+"/api/vms", nil)
		if err == nil {
			if node.Token != "" {
				req.Header.Set("Authorization", "Bearer "+node.Token)
			}
			resp, err := client.Do(req)
			if err == nil {
				var payload struct {
					Items []struct {
						ID   string `json:"id"`
						VMID int    `json:"vmid"`
						IP   string `json:"ip"`
					} `json:"items"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&payload)
				_ = resp.Body.Close()
				for _, item := range payload.Items {
					if item.ID == vmID || (item.VMID > 0 && item.VMID == vm.VMID) {
						if strings.TrimSpace(item.IP) != "" {
							guestIP = strings.TrimSpace(item.IP)
						}
						break
					}
				}
			}
		}
		if guestIP == "" {
			continue
		}
		if request.DNS1 != "" || request.DNS2 != "" {
			dnsPayload, _ := json.Marshal(map[string]string{"ip": guestIP, "dns1": request.DNS1, "dns2": request.DNS2})
			dnsReq, dnsErr := http.NewRequest(http.MethodPost, strings.TrimRight(node.URL, "/")+"/api/vms/"+url.PathEscape(vmID)+"/guest/dns", strings.NewReader(string(dnsPayload)))
			if dnsErr == nil {
				dnsReq.Header.Set("Content-Type", "application/json")
				if node.Token != "" {
					dnsReq.Header.Set("Authorization", "Bearer "+node.Token)
				}
				if dnsResp, dnsErr := client.Do(dnsReq); dnsErr == nil {
					_ = dnsResp.Body.Close()
				}
			}
		}
		earlyPassword, _ := json.Marshal(map[string]string{"ip": guestIP, "token": "", "username": username, "password": request.Password})
		earlyPasswordReq, earlyPasswordErr := http.NewRequest(http.MethodPost, strings.TrimRight(node.URL, "/")+"/api/vms/"+url.PathEscape(vmID)+"/guest/password", strings.NewReader(string(earlyPassword)))
		if earlyPasswordErr == nil {
			earlyPasswordReq.Header.Set("Content-Type", "application/json")
			if node.Token != "" {
				earlyPasswordReq.Header.Set("Authorization", "Bearer "+node.Token)
			}
			if earlyPasswordResp, earlyPasswordErr := client.Do(earlyPasswordReq); earlyPasswordErr == nil {
				_ = earlyPasswordResp.Body.Close()
			}
		}
		if request.NetworkMode == "private" && request.IP != "" && request.Gateway != "" && request.PrefixLen > 0 {
			networkPayload, _ := json.Marshal(map[string]any{"ip": request.IP, "target_ip": guestIP, "prefix": request.PrefixLen, "gateway": request.Gateway, "dns1": request.DNS1, "dns2": request.DNS2, "token": ""})
			networkReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(node.URL, "/")+"/api/vms/"+url.PathEscape(vmID)+"/guest/network", strings.NewReader(string(networkPayload)))
			if err == nil {
				networkReq.Header.Set("Content-Type", "application/json")
				if node.Token != "" {
					networkReq.Header.Set("Authorization", "Bearer "+node.Token)
				}
				if networkResp, networkErr := client.Do(networkReq); networkErr == nil {
					_ = networkResp.Body.Close()
					if networkResp.StatusCode < 300 {
						guestIP = request.IP
						time.Sleep(2 * time.Second)
					}
				}
			}
		}
		if strings.HasPrefix(strings.ToLower(request.OSType), "win") && request.DiskGB > 0 {
			resizePayload, _ := json.Marshal(map[string]string{"ip": guestIP, "token": ""})
			resizeReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(node.URL, "/")+"/api/vms/"+url.PathEscape(vmID)+"/guest/resize", strings.NewReader(string(resizePayload)))
			if err == nil {
				resizeReq.Header.Set("Content-Type", "application/json")
				if node.Token != "" {
					resizeReq.Header.Set("Authorization", "Bearer "+node.Token)
				}
				if resizeResp, resizeErr := client.Do(resizeReq); resizeErr == nil {
					_ = resizeResp.Body.Close()
				}
			}
		}
		if request.DataDiskGB > 0 {
			dataPayload, _ := json.Marshal(map[string]string{"ip": guestIP, "token": ""})
			dataReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(node.URL, "/")+"/api/vms/"+url.PathEscape(vmID)+"/guest/datadisk", strings.NewReader(string(dataPayload)))
			if err == nil {
				dataReq.Header.Set("Content-Type", "application/json")
				if node.Token != "" {
					dataReq.Header.Set("Authorization", "Bearer "+node.Token)
				}
				if dataResp, dataErr := client.Do(dataReq); dataErr == nil {
					_ = dataResp.Body.Close()
				}
			}
		}
		payload, _ := json.Marshal(map[string]string{"ip": guestIP, "token": "", "username": username, "password": request.Password})
		req, err = http.NewRequest(http.MethodPost, strings.TrimRight(node.URL, "/")+"/api/vms/"+url.PathEscape(vmID)+"/guest/password", strings.NewReader(string(payload)))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if node.Token != "" {
			req.Header.Set("Authorization", "Bearer "+node.Token)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 300 {
			return
		}
	}
}
func (c *Controller) reconcileNAT(node *nodeState, response []byte, allocation natAllocationRequest) {
	var vm struct {
		ID   string `json:"id"`
		VMID int    `json:"vmid"`
	}
	if err := json.Unmarshal(response, &vm); err != nil {
		return
	}
	vmID := strings.TrimSpace(vm.ID)
	if vmID == "" && vm.VMID > 0 {
		vmID = fmt.Sprintf("%d", vm.VMID)
	}
	if vmID == "" {
		return
	}
	actualIP := ""
	for attempt := 0; attempt < 12; attempt++ {
		time.Sleep(10 * time.Second)
		req, err := http.NewRequest(http.MethodGet, strings.TrimRight(node.URL, "/")+"/api/vms", nil)
		if err != nil {
			return
		}
		if node.Token != "" {
			req.Header.Set("Authorization", "Bearer "+node.Token)
		}
		resp, err := c.client.Do(req)
		if err != nil {
			continue
		}
		var payload struct {
			Items []struct {
				ID   string `json:"id"`
				VMID int    `json:"vmid"`
				IP   string `json:"ip"`
			} `json:"items"`
		}
		err = json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()
		if err != nil {
			continue
		}
		for _, item := range payload.Items {
			if item.ID == vmID || (item.VMID > 0 && item.VMID == vm.VMID) {
				actualIP = strings.TrimSpace(item.IP)
				break
			}
		}
		if actualIP != "" {
			break
		}
	}
	if actualIP == "" || actualIP == allocation.InternalIP {
		return
	}
	natReq, err := http.NewRequest(http.MethodGet, strings.TrimRight(node.URL, "/")+"/api/nat", nil)
	if err != nil {
		return
	}
	if node.Token != "" {
		natReq.Header.Set("Authorization", "Bearer "+node.Token)
	}
	natResp, err := c.client.Do(natReq)
	if err != nil {
		return
	}
	var rules struct {
		Items []struct {
			ID         string `json:"id"`
			InternalIP string `json:"internal_ip"`
		} `json:"items"`
	}
	_ = json.NewDecoder(natResp.Body).Decode(&rules)
	_ = natResp.Body.Close()
	for _, rule := range rules.Items {
		if rule.InternalIP != allocation.InternalIP || rule.ID == "" {
			continue
		}
		req, err := http.NewRequest(http.MethodDelete, strings.TrimRight(node.URL, "/")+"/api/nat/"+url.PathEscape(rule.ID), nil)
		if err != nil {
			continue
		}
		if node.Token != "" {
			req.Header.Set("Authorization", "Bearer "+node.Token)
		}
		if resp, err := c.client.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}
	allocation.InternalIP = actualIP
	retry := allocation
	c.allocateNAT(node, retry)
}
func (c *Controller) allocateNAT(node *nodeState, allocation natAllocationRequest) {
	raw, err := json.Marshal(allocation)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(node.URL, "/")+"/api/nat/allocate", strings.NewReader(string(raw)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if node.Token != "" {
		req.Header.Set("Authorization", "Bearer "+node.Token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

func (c *Controller) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func peerCertificate(r *http.Request) (*tls.Certificate, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil, fmt.Errorf("client certificate is required")
	}

	cert := r.TLS.PeerCertificates[0]

	return &tls.Certificate{Certificate: [][]byte{cert.Raw}, Leaf: cert}, nil
}

func certificateFingerprint(cert *tls.Certificate) string {
	if cert == nil || len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])

	return hex.EncodeToString(sum[:])
}

func probeHealth(client *http.Client, baseURL, token string) bool {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return false
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func ParseNodes(raw string) ([]Node, error) {
	var nodes []Node
	if strings.TrimSpace(raw) == "" {
		return []Node{
			{ID: "pve-hk-01", Name: "PVE-HK-01", Provider: "pve", Region: "香港", URL: "http://127.0.0.1:8443"},
			{ID: "pve-jp-01", Name: "PVE-JP-01", Provider: "pve", Region: "日本", URL: "http://127.0.0.1:8445"},
			{ID: "hv-us-01", Name: "HV-US-01", Provider: "hyperv", Region: "美国", URL: "http://127.0.0.1:8444"},
		}, nil
	}
	if err := json.Unmarshal([]byte(raw), &nodes); err != nil {
		return nil, fmt.Errorf("invalid DEVIY_NODES JSON: %w", err)
	}

	return nodes, nil
}
