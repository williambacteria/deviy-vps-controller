package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type PVEConfig struct {
	BaseURL  string
	Token    string
	Node     string
	Storage  string
	Bridge   string
	Insecure bool
	Timeout  time.Duration
}

type PVE struct {
	cfg    PVEConfig
	client *http.Client
	Runner CommandRunner
}

func NewPVE(cfg PVEConfig) (*PVE, error) {
	if cfg.BaseURL == "" || cfg.Token == "" || cfg.Node == "" {
		return nil, errors.New("PVE 需要 baseURL、token 和 node")
	}
	if cfg.Storage == "" {
		cfg.Storage = "local-lvm"
	}
	if cfg.Bridge == "" {
		cfg.Bridge = "vmbr0"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: cfg.Insecure} //nolint:gosec // 自签 PVE 证书是常见部署方式，显式开关。
	transport.ResponseHeaderTimeout = cfg.Timeout

	return &PVE{
		cfg: cfg,
		client: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
		Runner: osCommandRunner{},
	}, nil
}

func (p *PVE) Name() string {
	return "pve"
}

func (p *PVE) Diagnose(ctx context.Context) []Diagnostic {
	checks := []Diagnostic{}

	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if err := p.do(ctx, http.MethodGet, "/api2/json/nodes", nil, &nodes); err != nil {
		return append(checks, Diagnostic{Name: "pve.token", Status: "failed", Message: "API Token 或地址不可用：" + err.Error()})
	}
	checks = append(checks, Diagnostic{Name: "pve.token", Status: "ok", Message: fmt.Sprintf("API Token 可用，可见 %d 个节点", len(nodes))})

	foundNode := false
	for _, node := range nodes {
		if node.Node == p.cfg.Node {
			foundNode = true

			break
		}
	}
	if !foundNode {
		checks = append(checks, Diagnostic{Name: "pve.node", Status: "failed", Message: "配置的节点不存在：" + p.cfg.Node})
	} else {
		checks = append(checks, Diagnostic{Name: "pve.node", Status: "ok", Message: "节点存在：" + p.cfg.Node})
	}

	var storages []struct {
		Storage string `json:"storage"`
	}
	if err := p.do(ctx, http.MethodGet, "/api2/json/nodes/"+url.PathEscape(p.cfg.Node)+"/storage", nil, &storages); err != nil {
		checks = append(checks, Diagnostic{Name: "pve.storage", Status: "failed", Message: "读取存储失败：" + err.Error()})
	} else {
		foundStorage := false
		for _, storage := range storages {
			if storage.Storage == p.cfg.Storage {
				foundStorage = true

				break
			}
		}
		if foundStorage {
			checks = append(checks, Diagnostic{Name: "pve.storage", Status: "ok", Message: "存储存在：" + p.cfg.Storage})
		} else {
			checks = append(checks, Diagnostic{Name: "pve.storage", Status: "failed", Message: "存储不存在或当前 Token 无权访问：" + p.cfg.Storage})
		}
	}

	var networks []struct {
		Iface string `json:"iface"`
	}
	if err := p.do(ctx, http.MethodGet, "/api2/json/nodes/"+url.PathEscape(p.cfg.Node)+"/network", nil, &networks); err != nil {
		checks = append(checks, Diagnostic{Name: "pve.bridge", Status: "failed", Message: "读取网络失败：" + err.Error()})
	} else {
		foundBridge := false
		for _, network := range networks {
			if network.Iface == p.cfg.Bridge {
				foundBridge = true

				break
			}
		}
		if foundBridge {
			checks = append(checks, Diagnostic{Name: "pve.bridge", Status: "ok", Message: "网桥存在：" + p.cfg.Bridge})
		} else {
			checks = append(checks, Diagnostic{Name: "pve.bridge", Status: "failed", Message: "网桥不存在：" + p.cfg.Bridge})
		}
	}

	return checks
}

func (p *PVE) List(ctx context.Context) ([]VM, error) {
	var data []struct {
		VMID    int    `json:"vmid"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		CPUs    int    `json:"cpus"`
		MaxMem  int64  `json:"maxmem"`
		MaxDisk int64  `json:"maxdisk"`
		Uptime  int64  `json:"uptime"`
	}

	if err := p.do(ctx, http.MethodGet, "/api2/json/nodes/"+url.PathEscape(p.cfg.Node)+"/qemu", nil, &data); err != nil {
		return nil, err
	}

	vms := make([]VM, 0, len(data))
	for _, item := range data {
		vm := VM{
			ID:       strconv.Itoa(item.VMID),
			VMID:     item.VMID,
			Name:     item.Name,
			Status:   item.Status,
			Cores:    item.CPUs,
			MemoryMB: int(item.MaxMem / 1024 / 1024),
			DiskGB:   int(item.MaxDisk / 1024 / 1024 / 1024),
			Node:     p.cfg.Node,
			Uptime:   item.Uptime,
		}
		p.fillDiskSizes(ctx, item.VMID, &vm)
		vms = append(vms, vm)
	}

	return vms, nil
}

func (p *PVE) Create(ctx context.Context, input CreateVMInput) (VM, error) {
	if input.Name == "" {
		return VM{}, errors.New("name is required")
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
	if input.Storage == "" {
		input.Storage = p.cfg.Storage
	}
	if input.Bridge == "" {
		input.Bridge = p.cfg.Bridge
	}
	if input.OSType == "" {
		input.OSType = "l26"
	}
	if strings.TrimSpace(input.Image) != "" {
		return p.createFromImage(ctx, input)
	}
	if strings.TrimSpace(input.Template) != "" {
		return p.createFromTemplate(ctx, input)
	}

	nextID, err := p.nextID(ctx)
	if err != nil {
		return VM{}, err
	}

	net0 := fmt.Sprintf("virtio,bridge=%s", input.Bridge)
	if input.IP != "" {
		prefix := input.PrefixLen
		if prefix <= 0 || prefix > 32 {
			prefix = 24
		}
		net0 += fmt.Sprintf(",ip=%s/%d", input.IP, prefix)
		if input.Gateway != "" {
			net0 += ",gw=" + input.Gateway
		}
	}
	disk := fmt.Sprintf("%s:%d", input.Storage, input.DiskGB)
	if input.SystemDiskMinMBps > 0 {
		disk += fmt.Sprintf(",mbps_rd=%d,mbps_wr=%d", input.SystemDiskMinMBps, input.SystemDiskMinMBps)
	}
	if input.SystemDiskMaxMBps > 0 {
		disk += fmt.Sprintf(",mbps_rd_max=%d,mbps_wr_max=%d", input.SystemDiskMaxMBps, input.SystemDiskMaxMBps)
	}
	values := url.Values{
		"vmid":   {strconv.Itoa(nextID)},
		"name":   {input.Name},
		"cores":  {strconv.Itoa(input.Cores)},
		"memory": {strconv.Itoa(input.MemoryMB)},
		"scsihw": {"virtio-scsi-pci"},
		"scsi0":  {disk},
		"net0":   {net0},
		"ostype": {input.OSType},
		"agent":  {"1"},
	}
	if input.DataDiskGB > 0 {
		dataDisk := fmt.Sprintf("%s:%d", input.Storage, input.DataDiskGB)
		if input.DataDiskMinMBps > 0 {
			dataDisk += fmt.Sprintf(",mbps_rd=%d,mbps_wr=%d", input.DataDiskMinMBps, input.DataDiskMinMBps)
		}
		if input.DataDiskMaxMBps > 0 {
			dataDisk += fmt.Sprintf(",mbps_rd_max=%d,mbps_wr_max=%d", input.DataDiskMaxMBps, input.DataDiskMaxMBps)
		}
		values.Set("scsi1", dataDisk)
	}
	if dns := strings.TrimSpace(input.DNS1 + " " + input.DNS2); dns != "" {
		values.Set("nameserver", dns)
	}
	if input.ISOFile != "" {
		values.Set("ide2", input.ISOFile+",media=cdrom")
	}
	if input.Start {
		values.Set("start", "1")
	}

	if err := p.do(ctx, http.MethodPost, "/api2/json/nodes/"+url.PathEscape(p.cfg.Node)+"/qemu", values, nil); err != nil {
		return VM{}, err
	}

	return p.Status(ctx, strconv.Itoa(nextID))
}

func (p *PVE) createFromImage(ctx context.Context, input CreateVMInput) (VM, error) {
	source := strings.TrimSpace(input.Image)
	if source == "" {
		return VM{}, errors.New("PVE image path is required")
	}
	if !filepath.IsAbs(source) && strings.TrimSpace(input.SystemDiskDir) != "" {
		source = filepath.Join(input.SystemDiskDir, source)
	}
	if _, err := os.Stat(source); err != nil {
		return VM{}, fmt.Errorf("PVE qcow2 image is not readable: %w", err)
	}

	nextID, err := p.nextID(ctx)
	if err != nil {
		return VM{}, err
	}
	net0 := fmt.Sprintf("virtio,bridge=%s", input.Bridge)
	if input.IP != "" {
		prefix := input.PrefixLen
		if prefix <= 0 || prefix > 32 {
			prefix = 24
		}
		net0 += fmt.Sprintf(",ip=%s/%d", input.IP, prefix)
		if input.Gateway != "" {
			net0 += ",gw=" + input.Gateway
		}
	}
	values := url.Values{
		"vmid":   {strconv.Itoa(nextID)},
		"name":   {input.Name},
		"cores":  {strconv.Itoa(input.Cores)},
		"memory": {strconv.Itoa(input.MemoryMB)},
		"scsihw": {"virtio-scsi-pci"},
		"net0":   {net0},
		"ostype": {input.OSType},
		"agent":  {"1"},
	}
	if dns := strings.TrimSpace(input.DNS1 + " " + input.DNS2); dns != "" {
		values.Set("nameserver", dns)
	}
	if input.ISOFile != "" {
		values.Set("ide2", input.ISOFile+",media=cdrom")
	}
	if err := p.do(ctx, http.MethodPost, "/api2/json/nodes/"+url.PathEscape(p.cfg.Node)+"/qemu", values, nil); err != nil {
		return VM{}, err
	}

	runner := p.Runner
	if runner == nil {
		runner = osCommandRunner{}
	}
	importOutput, err := runner.Run(ctx, "qm", "importdisk", strconv.Itoa(nextID), source, input.Storage, "--format", "qcow2")
	if err != nil {
		return VM{}, fmt.Errorf("PVE qm importdisk failed: %w: %s", err, strings.TrimSpace(string(importOutput)))
	}
	volume := pveImportedVolume(string(importOutput))
	if volume == "" {
		return VM{}, fmt.Errorf("PVE qm importdisk did not return a volume: %s", strings.TrimSpace(string(importOutput)))
	}

	disk := volume
	if input.SystemDiskMinMBps > 0 {
		disk += fmt.Sprintf(",mbps_rd=%d,mbps_wr=%d", input.SystemDiskMinMBps, input.SystemDiskMinMBps)
	}
	if input.SystemDiskMaxMBps > 0 {
		disk += fmt.Sprintf(",mbps_rd_max=%d,mbps_wr_max=%d", input.SystemDiskMaxMBps, input.SystemDiskMaxMBps)
	}
	configValues := url.Values{"scsi0": {disk}}
	if input.DataDiskGB > 0 {
		dataDisk := fmt.Sprintf("%s:%d", input.Storage, input.DataDiskGB)
		if input.DataDiskMinMBps > 0 {
			dataDisk += fmt.Sprintf(",mbps_rd=%d,mbps_wr=%d", input.DataDiskMinMBps, input.DataDiskMinMBps)
		}
		if input.DataDiskMaxMBps > 0 {
			dataDisk += fmt.Sprintf(",mbps_rd_max=%d,mbps_wr_max=%d", input.DataDiskMaxMBps, input.DataDiskMaxMBps)
		}
		configValues.Set("scsi1", dataDisk)
	}
	configPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", url.PathEscape(p.cfg.Node), nextID)
	if err := p.do(ctx, http.MethodPost, configPath, configValues, nil); err != nil {
		return VM{}, err
	}
	if input.Start {
		startPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/start", url.PathEscape(p.cfg.Node), nextID)
		if err := p.do(ctx, http.MethodPost, startPath, nil, nil); err != nil {
			return VM{}, err
		}
	}

	return p.Status(ctx, strconv.Itoa(nextID))
}

var pveImportedDiskPattern = regexp.MustCompile(`Successfully imported disk as '([^']+)'`)

func pveImportedVolume(output string) string {
	match := pveImportedDiskPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return ""
	}
	volume := match[1]
	if strings.HasPrefix(volume, "unused") {
		if index := strings.Index(volume, ":"); index >= 0 {
			volume = volume[index+1:]
		}
	}

	return volume
}

func (p *PVE) createFromTemplate(ctx context.Context, input CreateVMInput) (VM, error) {
	nextID, err := p.nextID(ctx)
	if err != nil {
		return VM{}, err
	}

	values := url.Values{
		"newid": {strconv.Itoa(nextID)},
		"name":  {input.Name},
		"full":  {"1"},
	}
	if input.Storage != "" {
		values.Set("storage", input.Storage)
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%s/clone", url.PathEscape(p.cfg.Node), url.PathEscape(input.Template))
	if err := p.do(ctx, http.MethodPost, path, values, nil); err != nil {
		return VM{}, err
	}
	configValues := url.Values{}
	if input.IP != "" {
		prefix := input.PrefixLen
		if prefix <= 0 || prefix > 32 {
			prefix = 24
		}
		net0 := fmt.Sprintf("virtio,bridge=%s,ip=%s/%d", input.Bridge, input.IP, prefix)
		if input.Gateway != "" {
			net0 += ",gw=" + input.Gateway
		}
		configValues.Set("net0", net0)
	}
	if dns := strings.TrimSpace(input.DNS1 + " " + input.DNS2); dns != "" {
		configValues.Set("nameserver", dns)
	}
	if input.DataDiskGB > 0 {
		dataDisk := fmt.Sprintf("%s:%d", input.Storage, input.DataDiskGB)
		if input.DataDiskMinMBps > 0 {
			dataDisk += fmt.Sprintf(",mbps_rd=%d,mbps_wr=%d", input.DataDiskMinMBps, input.DataDiskMinMBps)
		}
		if input.DataDiskMaxMBps > 0 {
			dataDisk += fmt.Sprintf(",mbps_rd_max=%d,mbps_wr_max=%d", input.DataDiskMaxMBps, input.DataDiskMaxMBps)
		}
		configValues.Set("scsi1", dataDisk)
	}
	if input.ISOFile != "" {
		configValues.Set("ide2", input.ISOFile+",media=cdrom")
	}
	if len(configValues) > 0 {
		configPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", url.PathEscape(p.cfg.Node), nextID)
		if err := p.do(ctx, http.MethodPost, configPath, configValues, nil); err != nil {
			return VM{}, err
		}
	}
	if input.Start {
		startPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/start", url.PathEscape(p.cfg.Node), nextID)
		if err := p.do(ctx, http.MethodPost, startPath, nil, nil); err != nil {
			return VM{}, err
		}
	}

	return p.Status(ctx, strconv.Itoa(nextID))
}

func (p *PVE) Backup(ctx context.Context, id string, destination string) (string, error) {
	vmid, err := strconv.Atoi(id)
	if err != nil {
		return "", ErrVMNotFound
	}
	if destination == "" {
		destination = p.cfg.Storage
	}
	if destination == "" {
		return "", errors.New("backup storage is required")
	}
	values := url.Values{
		"vmid":     {strconv.Itoa(vmid)},
		"storage":  {destination},
		"mode":     {"snapshot"},
		"compress": {"zstd"},
	}
	var raw json.RawMessage
	path := fmt.Sprintf("/api2/json/nodes/%s/vzdump", url.PathEscape(p.cfg.Node))
	if err := p.do(ctx, http.MethodPost, path, values, &raw); err != nil {
		return "", err
	}

	return string(raw), nil
}

func (p *PVE) Status(ctx context.Context, id string) (VM, error) {
	vmid, err := strconv.Atoi(id)
	if err != nil {
		return VM{}, ErrVMNotFound
	}

	var data struct {
		VMID    int    `json:"vmid"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		CPUs    int    `json:"cpus"`
		MaxMem  int64  `json:"maxmem"`
		MaxDisk int64  `json:"maxdisk"`
		Uptime  int64  `json:"uptime"`
	}

	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/current", url.PathEscape(p.cfg.Node), vmid)
	if err := p.do(ctx, http.MethodGet, path, nil, &data); err != nil {
		if errors.Is(err, errNotFound) {
			return VM{}, ErrVMNotFound
		}

		return VM{}, err
	}

	vm := VM{
		ID:       id,
		VMID:     data.VMID,
		Name:     data.Name,
		Status:   data.Status,
		Cores:    data.CPUs,
		MemoryMB: int(data.MaxMem / 1024 / 1024),
		DiskGB:   int(data.MaxDisk / 1024 / 1024 / 1024),
		Node:     p.cfg.Node,
		Uptime:   data.Uptime,
	}
	p.fillDiskSizes(ctx, data.VMID, &vm)

	return vm, nil
}

var pveDiskSizePattern = regexp.MustCompile(`(?:size=)?(\d+(?:\.\d+)?)([GMT])`)

func (p *PVE) fillDiskSizes(ctx context.Context, vmid int, vm *VM) {
	var config map[string]any
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", url.PathEscape(p.cfg.Node), vmid)
	if err := p.do(ctx, http.MethodGet, path, nil, &config); err != nil {
		return
	}
	system := pveDiskSize(config["scsi0"])
	data := pveDiskSize(config["scsi1"])
	if system > 0 {
		vm.SystemDiskGB = system
		vm.DataDiskGB = data
		vm.DiskGB = system + data
	}
}

func pveDiskSize(value any) int {
	text, _ := value.(string)
	match := pveDiskSizePattern.FindStringSubmatch(text)
	if len(match) != 3 {
		return 0
	}
	size, _ := strconv.ParseFloat(match[1], 64)
	switch match[2] {
	case "T":
		return int(size * 1024)
	case "G":
		return int(size)
	case "M":
		return int(size / 1024)
	default:
		return 0
	}
}

func (p *PVE) Action(ctx context.Context, id string, action string) error {
	vmid, err := strconv.Atoi(id)
	if err != nil {
		return ErrVMNotFound
	}

	var command string
	switch action {
	case "start", "stop", "reboot", "shutdown":
		command = action
	default:
		return errors.New("unsupported action")
	}

	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/%s", url.PathEscape(p.cfg.Node), vmid, command)

	return p.do(ctx, http.MethodPost, path, url.Values{}, nil)
}

func (p *PVE) Delete(ctx context.Context, id string) error {
	vmid, err := strconv.Atoi(id)
	if err != nil {
		return ErrVMNotFound
	}

	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d", url.PathEscape(p.cfg.Node), vmid)

	return p.do(ctx, http.MethodDelete, path, nil, nil)
}

func (p *PVE) nextID(ctx context.Context) (int, error) {
	var raw json.RawMessage
	if err := p.do(ctx, http.MethodGet, "/api2/json/cluster/nextid", nil, &raw); err != nil {
		return 0, err
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return strconv.Atoi(asString)
	}

	var asNumber int
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return asNumber, nil
	}

	return 0, errors.New("无法解析 PVE nextid")
}

var errNotFound = errors.New("not found")

func (p *PVE) do(ctx context.Context, method, path string, values url.Values, out any) error {
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.cfg.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+p.cfg.Token)
	if values != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("PVE 请求失败: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("PVE 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	if out == nil {
		return nil
	}

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}

	return json.Unmarshal(envelope.Data, out)
}
