package server

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"deviy.cloud/agent/internal/metrics"
	"deviy.cloud/agent/internal/network"
	"deviy.cloud/agent/internal/provider"
)

//go:embed web/*
var webFS embed.FS

type Config struct {
	Version           string
	Provider          string
	NodeID            string
	NodeName          string
	Region            string
	Token             string
	Mode              string
	PVEURL            string
	PVEToken          string
	PVENode           string
	PVEStorage        string
	PVEBridge         string
	PVEInsecure       bool
	HyperVSwitch      string
	HyperVStorage     string
	KVMURI            string
	KVMStorage        string
	KVMNetwork        string
	KVMBridge         string
	KVMISODir         string
	KVMBackupDir      string
	NetworkBackend    string
	WANInterface      string
	NATCIDR           string
	AllowNetworkApply bool
	MetricsBackend    string
	MetricsInterface  string
	TLSConfig         *tls.Config
}

type App struct {
	cfg               Config
	started           time.Time
	mu                sync.RWMutex
	provider          provider.Provider
	providerErr       string
	network           network.Engine
	allowNetworkApply bool
	monitor           *metrics.Monitor
	hostMetrics       metrics.HostCollector
	usage             *usageTracker
	networkBackups    map[string]networkBackup
	nat               []NATRule
	firewall          []FirewallRule
	tasks             []Task
}

type networkBackup struct {
	ID        string
	Rules     []NATRule
	CreatedAt time.Time
}

type NATRule = network.NATRule

type FirewallRule = network.FirewallRule

type Task struct {
	ID        string `json:"id"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

func New(cfg Config) *App {
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if cfg.Provider == "" {
		cfg.Provider = "pve"
	}
	if cfg.NodeName == "" {
		cfg.NodeName = "deviy-node"
	}
	if cfg.NodeID == "" {
		cfg.NodeID = strings.ToLower(cfg.NodeName)
	}
	if cfg.Region == "" {
		cfg.Region = "香港"
	}
	if cfg.Mode == "" {
		cfg.Mode = "mock"
	}

	var p provider.Provider = provider.NewMock()
	providerErr := ""
	if cfg.Mode == "pve" {
		pve, err := provider.NewPVE(provider.PVEConfig{
			BaseURL:  cfg.PVEURL,
			Token:    cfg.PVEToken,
			Node:     cfg.PVENode,
			Storage:  cfg.PVEStorage,
			Bridge:   cfg.PVEBridge,
			Insecure: cfg.PVEInsecure,
		})
		if err != nil {
			providerErr = err.Error()
			cfg.Mode = "mock"
		} else {
			p = pve
		}
	}
	if cfg.Mode == "hyperv" {
		hyperv, err := provider.NewHyperV(provider.HyperVConfig{
			SwitchName: cfg.HyperVSwitch,
			Storage:    cfg.HyperVStorage,
		}, nil)
		if err != nil {
			providerErr = err.Error()
			cfg.Mode = "mock"
		} else {
			p = hyperv
		}
	}
	if cfg.Mode == "kvm" {
		p = provider.NewKVM(provider.KVMConfig{
			URI:       cfg.KVMURI,
			Storage:   cfg.KVMStorage,
			Network:   cfg.KVMNetwork,
			Bridge:    cfg.KVMBridge,
			ISODir:    cfg.KVMISODir,
			BackupDir: cfg.KVMBackupDir,
		})
	}

	var netEngine network.Engine = network.NewMock()
	switch cfg.NetworkBackend {
	case "nftables":
		netEngine = network.NewNftables(cfg.WANInterface, cfg.NATCIDR)
	case "windows":
		netEngine = network.NewWindowsWithConfig("DeviyNAT", cfg.NATCIDR)
	}

	var collector metrics.Collector = metrics.NewMock()
	switch cfg.MetricsBackend {
	case "proc":
		collector = metrics.NewProcNetDev(cfg.MetricsInterface)
	case "windows":
		collector = metrics.NewWindows(cfg.MetricsInterface)
	}
	monitor := metrics.NewMonitor(collector, 5*time.Second)
	go monitor.Run(context.Background())

	app := &App{
		cfg:               cfg,
		started:           time.Now(),
		provider:          p,
		providerErr:       providerErr,
		network:           netEngine,
		allowNetworkApply: cfg.AllowNetworkApply,
		monitor:           monitor,
		hostMetrics:       metrics.NewHostCollector(),
		networkBackups:    map[string]networkBackup{},
		nat: []NATRule{
			{ID: "nat-1001", ExternalIP: "203.0.113.88", ExternalPort: 20000, InternalIP: "10.0.0.12", InternalPort: 22, Protocol: "tcp", Status: "active", CreatedAt: time.Now().Add(-72 * time.Hour).Format(time.RFC3339)},
			{ID: "nat-1002", ExternalIP: "203.0.113.88", ExternalPort: 20001, InternalIP: "10.0.0.12", InternalPort: 80, Protocol: "tcp", Status: "active", CreatedAt: time.Now().Add(-48 * time.Hour).Format(time.RFC3339)},
			{ID: "nat-1003", ExternalIP: "203.0.113.88", ExternalPort: 20002, InternalIP: "10.0.0.12", InternalPort: 443, Protocol: "tcp", Status: "pending", CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339)},
		},
		firewall: []FirewallRule{
			{ID: "fw-1", Direction: "in", Action: "accept", Protocol: "tcp", Port: "22", Source: "0.0.0.0/0", Comment: "SSH"},
			{ID: "fw-2", Direction: "in", Action: "accept", Protocol: "tcp", Port: "80,443", Source: "0.0.0.0/0", Comment: "HTTP / HTTPS"},
			{ID: "fw-3", Direction: "in", Action: "drop", Protocol: "all", Port: "1-65535", Source: "0.0.0.0/0", Comment: "默认拒绝"},
		},
		tasks: []Task{
			{ID: "task-1", Operation: "sync_nat", Status: "succeeded", Message: "nftables 规则已原子替换", CreatedAt: time.Now().Add(-8 * time.Minute).Format(time.RFC3339)},
			{ID: "task-2", Operation: "bandwidth_sample", Status: "succeeded", Message: "采集 36 个实例流量", CreatedAt: time.Now().Add(-3 * time.Minute).Format(time.RFC3339)},
		},
	}

	if cfg.Mode != "mock" {
		app.nat = nil
		app.firewall = nil
		app.tasks = nil
		app.loadNAT()
		go app.applyPersistedNAT()
	}
	usagePath := os.Getenv("DEVIY_USAGE_PATH")
	if usagePath == "" {
		usagePath = "/var/lib/deviy/vm-usage.json"
	}
	app.usage = newUsageTracker(p, usagePath)
	go app.usage.Run()

	return app
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/api/overview", a.auth(a.handleOverview))
	mux.HandleFunc("/api/bandwidth", a.auth(a.handleBandwidth))
	mux.HandleFunc("/api/nat", a.auth(a.handleNAT))
	mux.HandleFunc("/api/nat/allocate", a.auth(a.handleNATAllocate))
	mux.HandleFunc("/api/nat/sync", a.auth(a.handleNATSync))
	mux.HandleFunc("/api/nat/", a.auth(a.handleNATItem))
	mux.HandleFunc("/api/firewall", a.auth(a.handleFirewall))
	mux.HandleFunc("/api/tasks", a.auth(a.handleTasks))
	mux.HandleFunc("/api/vms", a.auth(a.handleVMs))
	mux.HandleFunc("/api/vms/", a.auth(a.handleVMItem))
	mux.HandleFunc("/api/isos", a.auth(a.handleISOs))
	mux.HandleFunc("/api/upload/iso", a.auth(a.handleUpload))
	mux.HandleFunc("/api/upload/template", a.auth(a.handleUpload))
	mux.HandleFunc("/api/templates/delete", a.auth(a.handleTemplateDelete))
	mux.HandleFunc("/api/network/preview", a.auth(a.handleNetworkPreview))
	mux.HandleFunc("/api/network/apply", a.auth(a.handleNetworkApply))
	mux.HandleFunc("/api/network/verify", a.auth(a.handleNetworkVerify))
	mux.HandleFunc("/api/network/repair", a.auth(a.handleNetworkRepair))
	mux.HandleFunc("/api/network/rollback", a.auth(a.handleNetworkRollback))
	mux.HandleFunc("/api/diagnostics", a.auth(a.handleDiagnostics))

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	return a.logging(mux)
}

func (a *App) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         a.cfg.TLSConfig,
	}
	if a.cfg.TLSConfig != nil {
		return srv.ListenAndServeTLS("", "")
	}

	return srv.ListenAndServe()
}

func (a *App) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.Token == "" {
			next(w, r)
			return
		}

		header := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(header), []byte(a.cfg.Token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		next(w, r)
	}
}

func (a *App) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		_ = start
	})
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"version":  a.cfg.Version,
		"provider": a.cfg.Provider,
		"node":     a.cfg.NodeName,
		"node_id":  a.cfg.NodeID,
		"uptime":   int(time.Since(a.started).Seconds()),
	})
}

func (a *App) HeartbeatSnapshot() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	vms, _ := a.provider.List(ctx)
	running := 0
	for _, vm := range vms {
		if vm.Status == "running" {
			running++
		}
	}
	bandwidth := a.monitor.Snapshot()

	return map[string]any{
		"status":            "online",
		"provider":          a.cfg.Provider,
		"provider_mode":     a.cfg.Mode,
		"region":            a.cfg.Region,
		"vms_total":         len(vms),
		"vms_running":       running,
		"nat_count":         len(a.nat),
		"bandwidth_rx_mbps": bandwidth.RxMbps,
		"bandwidth_tx_mbps": bandwidth.TxMbps,
		"uptime_seconds":    int(time.Since(a.started).Seconds()),
	}
}

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	vms, _ := a.provider.List(ctx)
	running := 0
	for _, vm := range vms {
		if vm.Status == "running" {
			running++
		}
	}

	hostStats, hostErr := a.hostMetrics.Read(ctx)
	capacity := map[string]any{
		"cpu_cores":   hostStats.CPUCores,
		"memory_gb":   hostStats.MemoryTotalMB / 1024,
		"storage_gb":  int64(hostStats.DiskTotalGB),
		"used_vms":    len(vms),
		"running_vms": running,
	}
	hostPayload := map[string]any{
		"cpu_model":       hostStats.CPUModel,
		"cpu_cores":       hostStats.CPUCores,
		"cpu_usage":       hostStats.CPUUsage,
		"memory_total_mb": hostStats.MemoryTotalMB,
		"memory_used_mb":  hostStats.MemoryUsedMB,
		"memory_usage":    hostStats.MemoryUsage,
		"disk_total_gb":   hostStats.DiskTotalGB,
		"disk_used_gb":    hostStats.DiskUsedGB,
		"disk_usage":      hostStats.DiskUsage,
		"disks":           hostStats.Disks,
		"updated_at":      hostStats.UpdatedAt,
	}
	if hostErr != nil {
		hostPayload["error"] = hostErr.Error()
	}
	bandwidth := a.monitor.Snapshot()

	writeJSON(w, http.StatusOK, map[string]any{
		"node":            a.cfg.NodeName,
		"provider":        a.cfg.Provider,
		"provider_mode":   a.cfg.Mode,
		"provider_error":  a.providerErr,
		"region":          a.cfg.Region,
		"version":         a.cfg.Version,
		"status":          "online",
		"uptime_seconds":  int(time.Since(a.started).Seconds()),
		"last_heartbeat":  time.Now().Format(time.RFC3339),
		"capacity":        capacity,
		"host":            hostPayload,
		"vms_total":       len(vms),
		"vms_running":     running,
		"nat_count":       len(a.nat),
		"firewall_count":  len(a.firewall),
		"bandwidth_mbps":  1000,
		"bandwidth_usage": round2(bandwidth.RxMbps + bandwidth.TxMbps),
		"bandwidth":       bandwidth,
	})
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func (a *App) handleISOs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	if dir == "" {
		dir = "iso"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "items": []any{}, "error": err.Error()})
		return
	}
	items := make([]map[string]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".iso") {
			continue
		}
		items = append(items, map[string]string{
			"name": entry.Name(),
			"path": filepath.Join(dir, entry.Name()),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["name"] < items[j]["name"] })
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "items": items})
}

func (a *App) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	kind := strings.TrimPrefix(r.URL.Path, "/api/upload/")
	dir := strings.TrimSpace(a.cfg.KVMISODir)
	if dir == "" {
		dir = "/var/lib/libvirt/images/iso"
	}
	extensions := map[string]bool{".iso": true}
	switch kind {
	case "iso":
		extensions = map[string]bool{".iso": true}
	case "template":
		dir = filepath.Join(filepath.Dir(dir), "templates")
		extensions = map[string]bool{".qcow2": true, ".img": true, ".raw": true}
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown upload kind"})
		return
	}
	name := filepath.Base(strings.TrimSpace(r.URL.Query().Get("filename")))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}
	if !extensions[strings.ToLower(filepath.Ext(name))] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported file extension"})
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	temp, err := os.CreateTemp(dir, "."+name+".part-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	written, copyErr := io.Copy(temp, r.Body)
	closeErr := temp.Close()
	if copyErr != nil || closeErr != nil {
		if copyErr == nil {
			copyErr = closeErr
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": copyErr.Error()})
		return
	}
	finalPath := filepath.Join(dir, name)
	if err := os.Rename(tempPath, finalPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "uploaded",
		"kind":   kind,
		"name":   name,
		"path":   finalPath,
		"size":   written,
	})
}
func (a *App) handleVMs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	switch r.Method {
	case http.MethodGet:
		vms, err := a.provider.List(ctx)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": vms, "provider": a.provider.Name()})
	case http.MethodPost:
		var input provider.CreateVMInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if input.Cores <= 0 {
			input.Cores = 2
		}
		if input.MemoryMB <= 0 {
			input.MemoryMB = 4096
		}
		if input.DiskGB <= 0 {
			input.DiskGB = 40
		}
		if input.Name == "" {
			input.Name = fmt.Sprintf("deviy-%d", time.Now().Unix()%100000)
		}

		vm, err := a.provider.Create(ctx, input)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		a.recordTask("vm.create", "succeeded", fmt.Sprintf("%s (VMID %d) 已创建", vm.Name, vm.VMID))
		writeJSON(w, http.StatusCreated, vm)
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) handleVMTemplate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	provider, ok := a.provider.(provider.TemplateProvider)
	if !ok {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "当前虚拟化平台不支持转换为模板"})
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&input)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	info, err := provider.CreateTemplate(ctx, id, input.Name)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	a.recordTask("vm.template", "succeeded", fmt.Sprintf("%s -> %s", id, info.Name))
	writeJSON(w, http.StatusCreated, info)
}
func (a *App) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&input); err != nil || strings.TrimSpace(input.Path) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}
	templateProvider, ok := a.provider.(provider.TemplateProvider)
	if !ok {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "当前虚拟化平台不支持删除模板文件"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := templateProvider.DeleteTemplate(ctx, input.Path); err != nil {
		if errors.Is(err, provider.ErrTemplateInUse) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "模板正在被虚拟机使用，请先删除使用该模板的所有虚拟机"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
func (a *App) handleVMBackup(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		BackupDir string `json:"backup_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if input.BackupDir == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "backup_dir is required"})
		return
	}
	backuper, ok := a.provider.(provider.Backuper)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider does not support backup"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	result, err := backuper.Backup(ctx, id, input.BackupDir)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	a.recordTask("vm.backup", "succeeded", fmt.Sprintf("VM %s 备份完成", id))
	writeJSON(w, http.StatusOK, map[string]any{"status": "accepted", "id": id, "result": result})
}

func (a *App) handleVMVNC(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	vncProvider, ok := a.provider.(provider.VNCProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider does not support VNC"})
		return
	}
	info, err := vncProvider.VNC(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (a *App) handleVMStats(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	statsProvider, ok := a.provider.(provider.StatsProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider does not support VM stats"})
		return
	}
	stats, err := statsProvider.Stats(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (a *App) handleVMReinstall(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	reinstaller, ok := a.provider.(provider.ReinstallProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider does not support reinstall"})
		return
	}
	var input struct {
		Image   string `json:"image"`
		ISOFile string `json:"iso_file"`
		OSType  string `json:"ostype"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	vm, err := reinstaller.Reinstall(r.Context(), id, input.Image, input.ISOFile, input.OSType)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, vm)
}

func (a *App) handleVMCDROM(w http.ResponseWriter, r *http.Request, id string) {
	cdromProvider, ok := a.provider.(provider.CDROMProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider does not support CDROM"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		info, err := cdromProvider.CDROM(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, info)
	case http.MethodPost:
		var input struct {
			ISOFile string `json:"iso_file"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.ISOFile == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "iso_file is required"})
			return
		}
		if err := cdromProvider.MountISO(r.Context(), id, input.ISOFile); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "mounted", "iso_file": input.ISOFile})
	case http.MethodDelete:
		if err := cdromProvider.EjectISO(r.Context(), id); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ejected"})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) handleVMKey(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	keyProvider, ok := a.provider.(provider.KeyProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider does not support key injection"})
		return
	}
	var input struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if input.Key == "" {
		input.Key = "KEY_ENTER"
	}
	if err := keyProvider.SendKey(r.Context(), id, input.Key); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "key": input.Key})
}

func (a *App) handleVMUsage(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.usage.Get(id))
}

func (a *App) handleVMBootOrder(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	bootProvider, ok := a.provider.(provider.BootOrderProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider does not support boot order"})
		return
	}
	var input struct {
		Order   []string `json:"order"`
		ISOFile string   `json:"iso_file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := bootProvider.SetBootOrder(r.Context(), id, input.Order, input.ISOFile); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "order": input.Order, "iso_file": input.ISOFile})
}

func (a *App) handleVMGuestPassword(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		IP       string `json:"ip"`
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	ip := net.ParseIP(input.IP)
	if ip == nil || !privateIPv4(ip) || input.Username == "" || len(input.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid guest target or credentials"})
		return
	}
	client := &http.Client{Timeout: 20 * time.Second}
	body, _ := json.Marshal(map[string]string{"username": input.Username, "password": input.Password})
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://"+net.JoinHostPort(ip.String(), "8099")+"/password", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if input.Token != "" {
		req.Header.Set("X-Deviy-Token", input.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode >= 300 {
		log.Printf("guest password update failed vm=%s ip=%s status=%d error=%s", id, ip.String(), resp.StatusCode, strings.TrimSpace(string(responseBody)))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(responseBody))})
		return
	}
	log.Printf("guest password updated vm=%s ip=%s user=%s", id, ip.String(), input.Username)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(responseBody)
}

func (a *App) handleVMGuestResize(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		IP    string `json:"ip"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	ip := net.ParseIP(input.IP)
	if ip == nil || !privateIPv4(ip) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid guest target"})
		return
	}
	body, _ := json.Marshal(map[string]string{"ip": input.IP})
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://"+net.JoinHostPort(ip.String(), "8099")+"/resize", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if input.Token != "" {
		req.Header.Set("X-Deviy-Token", input.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(responseBody))})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(responseBody)
}
func (a *App) handleVMGuestDataDisk(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		IP    string `json:"ip"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	ip := net.ParseIP(input.IP)
	if ip == nil || !privateIPv4(ip) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid guest target"})
		return
	}
	body, _ := json.Marshal(map[string]string{"ip": input.IP})
	client := &http.Client{Timeout: 60 * time.Second}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://"+net.JoinHostPort(ip.String(), "8099")+"/datadisk", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if input.Token != "" {
		req.Header.Set("X-Deviy-Token", input.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(responseBody))})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(responseBody)
}
func (a *App) handleVMGuestDNS(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		IP   string `json:"ip"`
		DNS1 string `json:"dns1"`
		DNS2 string `json:"dns2"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	ip := net.ParseIP(input.IP)
	if ip == nil || !privateIPv4(ip) || (strings.TrimSpace(input.DNS1) == "" && strings.TrimSpace(input.DNS2) == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid dns target"})
		return
	}
	body, _ := json.Marshal(map[string]string{"dns1": input.DNS1, "dns2": input.DNS2})
	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://"+net.JoinHostPort(ip.String(), "8099")+"/dns", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(responseBody))})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(responseBody)
}

func (a *App) handleVMGuestNetwork(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		IP       string `json:"ip"`
		TargetIP string `json:"target_ip"`
		Prefix   int    `json:"prefix"`
		Gateway  string `json:"gateway"`
		DNS1     string `json:"dns1"`
		DNS2     string `json:"dns2"`
		Token    string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	target := net.ParseIP(input.IP)
	if input.TargetIP != "" {
		target = net.ParseIP(input.TargetIP)
	}
	newIP := net.ParseIP(input.IP)
	if target == nil || !privateIPv4(target) || newIP == nil || !privateIPv4(newIP) || input.Prefix < 1 || input.Prefix > 32 || net.ParseIP(input.Gateway) == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid network target"})
		return
	}
	body, _ := json.Marshal(map[string]any{"ip": input.IP, "prefix": input.Prefix, "gateway": input.Gateway, "dns1": input.DNS1, "dns2": input.DNS2})
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://"+net.JoinHostPort(target.String(), "8099")+"/network", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if input.Token != "" {
		req.Header.Set("X-Deviy-Token", input.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(responseBody))})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(responseBody)
}
func privateIPv4(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil {
		return false
	}

	return ip[0] == 10 || (ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31) || (ip[0] == 192 && ip[1] == 168)
}

func (a *App) handleVMBandwidth(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	bandwidthProvider, ok := a.provider.(provider.BandwidthProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider does not support bandwidth settings"})
		return
	}
	var input struct {
		UploadMbps   int `json:"upload_mbps"`
		DownloadMbps int `json:"download_mbps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := bandwidthProvider.SetBandwidth(r.Context(), id, input.UploadMbps, input.DownloadMbps); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "upload_mbps": input.UploadMbps, "download_mbps": input.DownloadMbps})
}

func (a *App) handleVMVNCWebSocket(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	vncProvider, ok := a.provider.(provider.VNCProvider)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider does not support VNC"})
		return
	}
	info, err := vncProvider.VNC(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	target, err := net.DialTimeout("tcp", net.JoinHostPort(info.Host, strconv.Itoa(info.Port)), 5*time.Second)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	ws, reader, err := upgradeWebSocket(w, r)
	if err != nil {
		_ = target.Close()
		return
	}
	relayWebSocket(ws, reader.Reader, target)
}

func (a *App) handleVMItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/vms/")
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[1] == "template" {
		a.handleVMTemplate(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "backup" {
		a.handleVMBackup(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "vnc" {
		a.handleVMVNC(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "vncws" {
		a.handleVMVNCWebSocket(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "stats" {
		a.handleVMStats(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "bandwidth" {
		a.handleVMBandwidth(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "reinstall" {
		a.handleVMReinstall(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "boot-order" {
		a.handleVMBootOrder(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "usage" {
		a.handleVMUsage(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "cdrom" {
		a.handleVMCDROM(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "key" {
		a.handleVMKey(w, r, parts[0])
		return
	}
	if len(parts) == 3 && parts[1] == "guest" && parts[2] == "password" {
		a.handleVMGuestPassword(w, r, parts[0])
		return
	}
	if len(parts) == 3 && parts[1] == "guest" && parts[2] == "network" {
		a.handleVMGuestNetwork(w, r, parts[0])
		return
	}
	if len(parts) == 3 && parts[1] == "guest" && parts[2] == "dns" {
		a.handleVMGuestDNS(w, r, parts[0])
		return
	}
	if len(parts) == 3 && parts[1] == "guest" && parts[2] == "datadisk" {
		a.handleVMGuestDataDisk(w, r, parts[0])
		return
	}
	if len(parts) == 3 && parts[1] == "guest" && parts[2] == "resize" {
		a.handleVMGuestResize(w, r, parts[0])
		return
	}
	if len(parts) != 2 || parts[1] != "action" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	id := parts[0]
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid vm id"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var input struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Action == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var err error
	if input.Action == "delete" {
		err = a.provider.Delete(ctx, id)
	} else {
		err = a.provider.Action(ctx, id, input.Action)
	}
	if err != nil {
		status := http.StatusBadGateway
		if err == provider.ErrVMNotFound {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	a.recordTask("vm."+input.Action, "succeeded", fmt.Sprintf("VM %s 操作完成", id))
	vm, err := a.provider.Status(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "accepted", "id": id})
		return
	}
	writeJSON(w, http.StatusOK, vm)
}

func (a *App) recordTask(operation, status, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tasks = append([]Task{{
		ID:        fmt.Sprintf("task-%d", time.Now().UnixNano()),
		Operation: operation,
		Status:    status,
		Message:   message,
		CreatedAt: time.Now().Format(time.RFC3339),
	}}, a.tasks...)
	if len(a.tasks) > 40 {
		a.tasks = a.tasks[:40]
	}
}

func (a *App) handleBandwidth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.monitor.Snapshot())
}

func (a *App) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	type check struct {
		Name      string `json:"name"`
		Status    string `json:"status"`
		Message   string `json:"message"`
		LatencyMS int64  `json:"latency_ms"`
	}

	checks := []check{}
	overall := "ok"

	started := time.Now()
	vms, err := a.provider.List(ctx)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		checks = append(checks, check{Name: "provider", Status: "failed", Message: err.Error(), LatencyMS: latency})
		overall = "failed"
	} else {
		checks = append(checks, check{Name: "provider", Status: "ok", Message: fmt.Sprintf("%s 返回 %d 台云机", a.provider.Name(), len(vms)), LatencyMS: latency})
	}

	if diagnoser, ok := a.provider.(provider.Diagnoser); ok {
		for _, diagnostic := range diagnoser.Diagnose(ctx) {
			checks = append(checks, check{
				Name:    diagnostic.Name,
				Status:  diagnostic.Status,
				Message: diagnostic.Message,
			})
			if diagnostic.Status == "failed" {
				overall = "failed"
			} else if diagnostic.Status == "warning" && overall == "ok" {
				overall = "warning"
			}
		}
	}

	metricsSnapshot := a.monitor.Snapshot()
	metricStatus := "ok"
	metricMessage := fmt.Sprintf("网卡 %s，采样点 %d 个", metricsSnapshot.Interface, len(metricsSnapshot.Points))
	if metricsSnapshot.Error != "" {
		metricStatus = "warning"
		metricMessage = metricsSnapshot.Error
		overall = "warning"
	}
	checks = append(checks, check{Name: "metrics", Status: metricStatus, Message: metricMessage})

	checks = append(checks, check{Name: "network", Status: "ok", Message: "网络执行层：" + a.network.Name()})
	checks = append(checks, check{Name: "mode", Status: "ok", Message: "Provider 模式：" + a.cfg.Mode})

	if a.cfg.TLSConfig != nil {
		checks = append(checks, check{Name: "tls", Status: "ok", Message: "HTTPS/mTLS 已启用"})
	} else {
		checks = append(checks, check{Name: "tls", Status: "warning", Message: "当前为 HTTP，生产环境应启用 mTLS"})
		if overall == "ok" {
			overall = "warning"
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     overall,
		"node":       a.cfg.NodeName,
		"node_id":    a.cfg.NodeID,
		"provider":   a.cfg.Provider,
		"version":    a.cfg.Version,
		"checked_at": time.Now().Format(time.RFC3339),
		"uptime":     int(time.Since(a.started).Seconds()),
		"checks":     checks,
		"vms_total":  len(vms),
		"nat_count":  len(a.nat),
		"network":    a.network.Name(),
		"metrics_if": metricsSnapshot.Interface,
	})
}

func (a *App) handleNetworkVerify(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "nat"
	}
	if kind != "nat" {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "当前仅支持 NAT 回读校验"})

		return
	}

	verifier, ok := a.network.(network.NATVerifier)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "当前网络后端不支持回读校验"})

		return
	}

	a.mu.RLock()
	nat := append([]NATRule(nil), a.nat...)
	a.mu.RUnlock()

	if len(nat) == 0 {
		writeJSON(w, http.StatusOK, network.Verification{
			Backend:  a.network.Name(),
			Verified: true,
		})

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	verification, err := verifier.VerifyNAT(ctx, nat[0].ExternalIP, nat)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})

		return
	}

	writeJSON(w, http.StatusOK, verification)
}

func (a *App) handleNetworkPreview(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "nat"
	}

	plan, err := a.buildNetworkPlan(kind)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, plan)
}

func (a *App) handleNetworkApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var input struct {
		Kind    string `json:"kind"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "必须确认后才能应用网络变更"})
		return
	}
	if !a.allowNetworkApply && a.network.Name() != "mock" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "当前 Agent 未开启网络变更，请使用预览或启动时加 --allow-network-apply"})
		return
	}

	plan, err := a.buildNetworkPlan(input.Kind)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	backupID := ""
	if plan.Kind == "nat" {
		backupID = a.saveNetworkBackup()
	}

	if err := a.network.Apply(ctx, plan); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	a.recordTask("network."+plan.Kind, "succeeded", plan.Summary)
	writeJSON(w, http.StatusOK, map[string]any{"status": "applied", "plan": plan, "backup_id": backupID})
}

func (a *App) handleNetworkRepair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)

		return
	}

	var input struct {
		Kind    string `json:"kind"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})

		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "必须确认后才能修复网络"})

		return
	}
	if input.Kind == "" {
		input.Kind = "nat"
	}
	if !a.allowNetworkApply && a.network.Name() != "mock" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "当前 Agent 未开启网络变更"})

		return
	}

	verifier, ok := a.network.(network.NATVerifier)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "当前网络后端不支持回读校验"})

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	a.mu.RLock()
	nat := append([]NATRule(nil), a.nat...)
	a.mu.RUnlock()
	if len(nat) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "没有可修复的 NAT 规则"})

		return
	}

	before, err := verifier.VerifyNAT(ctx, nat[0].ExternalIP, nat)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})

		return
	}
	if before.Verified {
		writeJSON(w, http.StatusOK, map[string]any{"status": "clean", "before": before})

		return
	}

	plan, err := a.buildNetworkPlan("nat")
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})

		return
	}
	backupID := a.saveNetworkBackup()
	if err := a.network.Apply(ctx, plan); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})

		return
	}

	after, _ := verifier.VerifyNAT(ctx, nat[0].ExternalIP, nat)
	a.recordTask("network.repair", map[bool]string{true: "succeeded", false: "failed"}[after.Verified], "NAT 漂移修复")
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "repaired",
		"before":    before,
		"after":     after,
		"backup_id": backupID,
	})
}

func (a *App) handleNetworkRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)

		return
	}

	var input struct {
		BackupID string `json:"backup_id"`
		Confirm  bool   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})

		return
	}
	if !input.Confirm || input.BackupID == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "backup_id 与 confirm 必填"})

		return
	}
	if !a.allowNetworkApply && a.network.Name() != "mock" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "当前 Agent 未开启网络变更"})

		return
	}

	a.mu.RLock()
	backup, ok := a.networkBackups[input.BackupID]
	a.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "备份不存在或已过期"})

		return
	}

	plan, err := a.network.PlanNAT(backup.Rules[0].ExternalIP, backup.Rules)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.network.Apply(ctx, plan); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})

		return
	}

	a.mu.Lock()
	a.nat = append([]NATRule(nil), backup.Rules...)
	delete(a.networkBackups, input.BackupID)
	a.mu.Unlock()
	a.recordTask("network.rollback", "succeeded", "回滚到 "+input.BackupID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "rolled_back", "backup_id": input.BackupID})
}

func (a *App) saveNetworkBackup() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	id := fmt.Sprintf("bak-%d", time.Now().UnixNano())
	a.networkBackups[id] = networkBackup{
		ID:        id,
		Rules:     append([]NATRule(nil), a.nat...),
		CreatedAt: time.Now(),
	}
	if len(a.networkBackups) > 10 {
		for key := range a.networkBackups {
			delete(a.networkBackups, key)

			break
		}
	}

	return id
}

func (a *App) buildNetworkPlan(kind string) (network.Plan, error) {
	a.mu.RLock()
	nat := append([]NATRule(nil), a.nat...)
	firewall := append([]FirewallRule(nil), a.firewall...)
	a.mu.RUnlock()

	switch kind {
	case "nat":
		if len(nat) == 0 {
			return network.Plan{}, fmt.Errorf("没有可应用的 NAT 规则")
		}

		return a.network.PlanNAT(nat[0].ExternalIP, nat)
	case "firewall":
		return a.network.PlanFirewall(firewall)
	default:
		return network.Plan{}, fmt.Errorf("不支持的网络类型: %s", kind)
	}
}

func (a *App) handleNAT(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.mu.RLock()
		defer a.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{"items": a.nat})
	case http.MethodPost:
		var input NATRule
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if input.ExternalIP == "" || input.InternalIP == "" || input.ExternalPort < 1 || input.InternalPort < 1 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "external_ip/internal_ip/ports required"})
			return
		}
		if input.Protocol == "" {
			input.Protocol = "tcp"
		}
		input.ID = fmt.Sprintf("nat-%d", time.Now().UnixNano())
		input.Status = "pending"
		input.CreatedAt = time.Now().Format(time.RFC3339)

		a.mu.Lock()
		a.nat = append(a.nat, input)
		a.tasks = append([]Task{{
			ID:        fmt.Sprintf("task-%d", time.Now().UnixNano()),
			Operation: "sync_nat",
			Status:    "queued",
			Message:   "等待写入 nftables / WinNAT",
			CreatedAt: time.Now().Format(time.RFC3339),
		}}, a.tasks...)
		a.mu.Unlock()

		writeJSON(w, http.StatusCreated, input)
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) handleNATAllocate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var input struct {
		ExternalIP   string `json:"external_ip"`
		InternalIP   string `json:"internal_ip"`
		InternalPort int    `json:"internal_port"`
		Protocol     string `json:"protocol"`
		PortMin      int    `json:"port_min"`
		PortMax      int    `json:"port_max"`
		ExcludePorts string `json:"exclude_ports"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if input.ExternalIP == "" || input.InternalIP == "" || input.InternalPort < 1 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "external_ip/internal_ip/internal_port required"})
		return
	}
	if input.Protocol == "" {
		input.Protocol = "tcp"
	}
	if input.PortMin <= 0 {
		input.PortMin = 10000
	}
	if input.PortMax <= 0 {
		input.PortMax = 30000
	}
	if input.PortMax < input.PortMin {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "port_max must be greater than port_min"})
		return
	}

	excluded := map[int]bool{}
	for _, raw := range strings.Split(input.ExcludePorts, ",") {
		port, err := strconv.Atoi(strings.TrimSpace(raw))
		if err == nil && port > 0 {
			excluded[port] = true
		}
	}

	a.mu.Lock()
	used := map[int]bool{}
	for _, rule := range a.nat {
		if rule.ExternalIP == input.ExternalIP && rule.Protocol == input.Protocol {
			used[rule.ExternalPort] = true
		}
	}
	port := 0
	for candidate := input.PortMin; candidate <= input.PortMax; candidate++ {
		if used[candidate] || excluded[candidate] {
			continue
		}
		port = candidate
		break
	}
	if port == 0 {
		a.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no available port in range"})
		return
	}
	rule := NATRule{
		ID:           fmt.Sprintf("nat-%d", time.Now().UnixNano()),
		ExternalIP:   input.ExternalIP,
		ExternalPort: port,
		InternalIP:   input.InternalIP,
		InternalPort: input.InternalPort,
		Protocol:     input.Protocol,
		Status:       "pending",
		CreatedAt:    time.Now().Format(time.RFC3339),
	}
	a.nat = append(a.nat, rule)
	a.mu.Unlock()
	a.saveNAT()

	if a.allowNetworkApply {
		a.mu.RLock()
		rules := append([]NATRule(nil), a.nat...)
		a.mu.RUnlock()
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		plan, planErr := a.network.PlanNAT(input.ExternalIP, rules)
		if planErr == nil {
			planErr = a.network.Apply(ctx, plan)
		}
		if planErr != nil {
			a.mu.Lock()
			filtered := a.nat[:0]
			for _, existing := range a.nat {
				if existing.ID != rule.ID {
					filtered = append(filtered, existing)
				}
			}
			a.nat = filtered
			a.mu.Unlock()
			a.saveNAT()
			a.recordTask("nat.allocate", "failed", planErr.Error())
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": planErr.Error()})
			return
		}
		rule.Status = "active"
		a.mu.Lock()
		for i := range a.nat {
			if a.nat[i].ID == rule.ID {
				a.nat[i] = rule
				break
			}
		}
		a.mu.Unlock()
		a.saveNAT()
	}

	a.recordTask("nat.allocate", "succeeded", fmt.Sprintf("%s:%d -> %s:%d", rule.ExternalIP, rule.ExternalPort, rule.InternalIP, rule.InternalPort))
	writeJSON(w, http.StatusCreated, rule)
}

func (a *App) handleNATSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var input struct {
		ExternalIP string    `json:"external_ip"`
		Mappings   []NATRule `json:"mappings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	a.mu.Lock()
	removed := 0
	kept := make([]NATRule, 0, len(a.nat))
	for _, rule := range a.nat {
		if rule.ExternalIP != input.ExternalIP {
			kept = append(kept, rule)
		} else {
			removed++
		}
	}
	for i := range input.Mappings {
		input.Mappings[i].ExternalIP = input.ExternalIP
		if input.Mappings[i].ID == "" {
			input.Mappings[i].ID = fmt.Sprintf("nat-%d-%d", time.Now().UnixNano(), i)
		}
		if input.Mappings[i].Status == "" {
			input.Mappings[i].Status = "active"
		}
		kept = append(kept, input.Mappings[i])
	}
	a.nat = kept
	total := len(a.nat)
	a.mu.Unlock()
	a.saveNAT()

	a.recordTask("sync_nat", "succeeded", fmt.Sprintf("%s 端口映射已同步（删除 %d，新增 %d，当前 %d）", input.ExternalIP, removed, len(input.Mappings), total))
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "synced",
		"external_ip": input.ExternalIP,
		"removed":     removed,
		"added":       len(input.Mappings),
		"count":       total,
	})
}

func (a *App) handleNATItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/nat/")
	if id == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	a.mu.Lock()
	index := -1
	var removed NATRule
	for i, rule := range a.nat {
		if rule.ID == id {
			index = i
			removed = rule
			break
		}
	}
	if index < 0 {
		a.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	a.nat = append(a.nat[:index], a.nat[index+1:]...)
	rules := append([]NATRule(nil), a.nat...)
	a.mu.Unlock()

	if a.allowNetworkApply {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		plan, err := a.network.PlanNAT(removed.ExternalIP, rules)
		if err == nil {
			err = a.network.Apply(ctx, plan)
		}
		if err != nil {
			a.mu.Lock()
			a.nat = append(a.nat, removed)
			a.mu.Unlock()
			a.saveNAT()
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
	}
	a.saveNAT()
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *App) handleFirewall(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.mu.RLock()
		defer a.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"policy": "default-deny-inbound",
			"items":  a.firewall,
			"engine": map[string]string{"pve": "nftables", "kvm": "nftables", "hyperv": "WinNAT + Windows Firewall"}[a.cfg.Provider],
		})
	case http.MethodPost:
		var input FirewallRule
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		input.ID = fmt.Sprintf("fw-%d", time.Now().UnixNano())
		if input.Action == "" {
			input.Action = "accept"
		}
		if input.Direction == "" {
			input.Direction = "in"
		}
		a.mu.Lock()
		a.firewall = append([]FirewallRule{input}, a.firewall...)
		a.mu.Unlock()
		writeJSON(w, http.StatusCreated, input)
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTasks(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"items": a.tasks})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
