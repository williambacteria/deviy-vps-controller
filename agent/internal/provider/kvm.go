package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type KVMConfig struct {
	URI       string
	Storage   string
	Network   string
	Bridge    string
	ISODir    string
	BackupDir string
}

type KVM struct {
	cfg    KVMConfig
	Runner CommandRunner
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func NewKVM(cfg KVMConfig) *KVM {
	if cfg.URI == "" {
		cfg.URI = "qemu:///system"
	}
	if cfg.Storage == "" {
		cfg.Storage = "/var/lib/libvirt/images/deviy"
	}
	if cfg.Network == "" {
		cfg.Network = "default"
	}
	if cfg.Bridge == "" {
		cfg.Bridge = "virbr0"
	}
	if cfg.ISODir == "" {
		cfg.ISODir = "/var/lib/libvirt/images/iso"
	}
	if cfg.BackupDir == "" {
		cfg.BackupDir = "/var/lib/libvirt/images/backup"
	}

	return &KVM{cfg: cfg, Runner: osCommandRunner{}}
}

func (k *KVM) Name() string {
	return "kvm"
}

func (k *KVM) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if k.Runner == nil {
		k.Runner = osCommandRunner{}
	}

	return k.Runner.Run(ctx, name, args...)
}

func (k *KVM) virsh(ctx context.Context, args ...string) ([]byte, error) {
	return k.run(ctx, "virsh", append([]string{"--connect", k.cfg.URI}, args...)...)
}

var kvmNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (k *KVM) List(ctx context.Context) ([]VM, error) {
	output, err := k.virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return nil, fmt.Errorf("libvirt list failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	vms := make([]VM, 0)
	for _, line := range strings.Split(string(output), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		vm, statusErr := k.Status(ctx, name)
		if statusErr != nil {
			continue
		}
		vms = append(vms, vm)
	}

	return vms, nil
}

func (k *KVM) Create(ctx context.Context, input CreateVMInput) (VM, error) {
	if !kvmNamePattern.MatchString(input.Name) {
		return VM{}, errors.New("invalid KVM domain name")
	}
	if input.Cores <= 0 {
		input.Cores = 2
	}
	if input.MemoryMB <= 0 {
		input.MemoryMB = 2048
	}
	if input.DiskGB <= 0 {
		input.DiskGB = 20
	}

	storage := strings.TrimSpace(input.SystemDiskDir)
	if storage == "" || !filepath.IsAbs(storage) {
		storage = k.cfg.Storage
	}
	if err := os.MkdirAll(storage, 0o755); err != nil {
		return VM{}, fmt.Errorf("create KVM storage dir: %w", err)
	}
	systemDisk := filepath.Join(storage, input.Name+".qcow2")
	if err := k.createDisk(ctx, systemDisk, input.Image, input.DiskGB); err != nil {
		return VM{}, err
	}

	diskBus := "virtio"
	osVariant := "generic"
	if strings.HasPrefix(strings.ToLower(input.OSType), "win") {
		diskBus = "sata"
		osVariant = "win2k22"
	}
	systemDiskArg := "path=" + systemDisk + ",format=qcow2,bus=" + diskBus
	args := []string{
		"--connect", k.cfg.URI,
		"--name", input.Name,
		"--memory", strconv.Itoa(input.MemoryMB),
		"--vcpus", strconv.Itoa(input.Cores),
		"--cpu", "host",
		"--disk", systemDiskArg,
		"--graphics", "vnc,listen=127.0.0.1",
		"--video", "qxl",
		"--noautoconsole",
		"--import",
		"--os-variant", osVariant,
	}

	if diskBus == "sata" {
		args = append(args, "--machine", "q35")
	}

	if input.ISOFile != "" {
		args = append(args, "--boot", "cdrom,hd")
	} else {
		args = append(args, "--boot", "hd")
	}

	if input.DataDiskGB > 0 {
		storageDir := strings.TrimSpace(input.DataDiskDir)
		if storageDir == "" || !filepath.IsAbs(storageDir) {
			storageDir = storage
		}
		if err := os.MkdirAll(storageDir, 0o755); err != nil {
			return VM{}, fmt.Errorf("create KVM data dir: %w", err)
		}
		dataDisk := filepath.Join(storageDir, input.Name+"-data.qcow2")
		if err := k.createBlankDisk(ctx, dataDisk, input.DataDiskGB); err != nil {
			return VM{}, err
		}
		args = append(args, "--disk", "path="+dataDisk+",format=qcow2,bus="+diskBus)
	}

	if input.IP != "" && input.NetworkMode != "public" && strings.HasPrefix(strings.ToLower(input.OSType), "win") {
		prefix := input.PrefixLen
		if prefix <= 0 || prefix > 32 {
			prefix = 24
		}
		dataFlag := "0"
		if input.DataDiskGB > 0 {
			dataFlag = "1"
		}
		serial := fmt.Sprintf("DV1;%s;%d;%s;%s", input.IP, prefix, input.Gateway, dataFlag)
		args = append(args, "--sysinfo", "smbios,system.serial="+serial)
	}
	if input.ISOFile != "" {
		args = append(args, "--cdrom", input.ISOFile)
	}

	seedISO := ""
	if strings.TrimSpace(input.SSHPublicKey) != "" {
		var seedErr error
		seedISO, seedErr = k.createCloudInit(ctx, storage, input)
		if seedErr != nil {
			_ = k.Delete(ctx, input.Name)
			return VM{}, seedErr
		}
		args = append(args, "--disk", "path="+seedISO+",device=cdrom,readonly=on")
	}

	if input.NetworkMode == "public" && input.Bridge != "" {
		args = append(args, "--network", "bridge="+input.Bridge+",model=virtio")
	} else {
		networkModel := "virtio"
		if diskBus == "sata" {
			networkModel = "e1000"
		}
		args = append(args, "--network", "network="+k.cfg.Network+",model="+networkModel)
	}

	output, err := k.run(ctx, "virt-install", args...)
	if err != nil {
		_ = k.Delete(ctx, input.Name)
		return VM{}, fmt.Errorf("virt-install failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if input.OSType != "" {
		_, _ = k.virsh(ctx, "desc", input.Name, "--new-desc", "deviy:ostype="+input.OSType, "--config")
	}

	if input.IP != "" && input.NetworkMode != "public" {
		if err := k.reserveDHCP(ctx, input.Name, input.IP); err != nil {
			log.Printf("KVM DHCP reservation warning for %s (%s): %v", input.Name, input.IP, err)
		}
	}

	if input.Start {
		_, _ = k.virsh(ctx, "autostart", input.Name)
	} else {
		_, _ = k.virsh(ctx, "destroy", input.Name)
	}

	k.storePassword(ctx, input.Name, input.Password)

	return k.Status(ctx, input.Name)
}

func (k *KVM) removeDHCPHost(ctx context.Context, name string) {
	k.removeDHCPBindings(ctx, name, "", "")
}

func (k *KVM) removeDHCPBindings(ctx context.Context, name, mac, ip string) {
	output, err := k.virsh(ctx, "net-dumpxml", k.cfg.Network)
	if err != nil {
		return
	}
	hostPattern := regexp.MustCompile(`<host\b[^>]*/>`)
	attrPattern := func(attr string) *regexp.Regexp {
		return regexp.MustCompile(`\b` + attr + `=["']([^"']*)["']`)
	}
	for _, host := range hostPattern.FindAllString(string(output), -1) {
		match := false
		for _, attr := range []string{"name", "mac", "ip"} {
			value := ""
			if m := attrPattern(attr).FindStringSubmatch(host); len(m) == 2 {
				value = m[1]
			}
			if (attr == "name" && name != "" && strings.EqualFold(value, name)) ||
				(attr == "mac" && mac != "" && strings.EqualFold(value, mac)) ||
				(attr == "ip" && ip != "" && value == ip) {
				match = true
				break
			}
		}
		if match {
			_, _ = k.virsh(ctx, "net-update", k.cfg.Network, "delete", "ip-dhcp-host", host, "--live", "--config")
		}
	}
}

func (k *KVM) reserveDHCP(ctx context.Context, name, ip string) error {
	if !kvmNamePattern.MatchString(name) {
		return errors.New("invalid KVM domain name")
	}
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("invalid KVM DHCP reservation IP: %s", ip)
	}
	output, err := k.virsh(ctx, "domiflist", name)
	if err != nil {
		return fmt.Errorf("read KVM MAC address: %w", err)
	}
	mac := ""
	macPattern := regexp.MustCompile(`([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}`)
	for _, line := range strings.Split(string(output), "\n") {
		if match := macPattern.FindString(line); match != "" {
			mac = match
			break
		}
	}
	if mac == "" {
		return errors.New("KVM domain has no MAC address")
	}
	dhcpName := name
	if !regexp.MustCompile(`^[A-Za-z]`).MatchString(dhcpName) {
		dhcpName = "vm-" + dhcpName
	}
	k.removeDHCPBindings(ctx, name, mac, ip)
	k.removeDHCPBindings(ctx, dhcpName, mac, ip)
	host := fmt.Sprintf(`<host mac='%s' name='%s' ip='%s'/>`, mac, dhcpName, ip)
	updateOutput, updateErr := k.virsh(ctx, "net-update", k.cfg.Network, "add", "ip-dhcp-host", host, "--live", "--config")
	if updateErr != nil {
		return fmt.Errorf("reserve KVM DHCP address: %w: %s", updateErr, strings.TrimSpace(string(updateOutput)))
	}

	return nil
}

func (k *KVM) createCloudInit(ctx context.Context, storage string, input CreateVMInput) (string, error) {
	key := strings.TrimSpace(input.SSHPublicKey)
	if !strings.HasPrefix(key, "ssh-") || strings.ContainsAny(key, "\r\n") {
		return "", errors.New("invalid SSH public key")
	}
	dir := filepath.Join(storage, input.Name+"-seed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cloud-init directory: %w", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", fmt.Errorf("set cloud-init directory permissions: %w", err)
	}
	userData := fmt.Sprintf(`#cloud-config
users:
  - name: deviy
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - %s
ssh_pwauth: false
`, key)
	metaData := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", input.Name, input.Name)
	userDataPath := filepath.Join(dir, "user-data")
	metaDataPath := filepath.Join(dir, "meta-data")
	if err := os.WriteFile(userDataPath, []byte(userData), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(metaDataPath, []byte(metaData), 0o600); err != nil {
		return "", err
	}
	seed := filepath.Join(dir, "seed.iso")
	output, err := k.run(ctx, "cloud-localds", seed, userDataPath, metaDataPath)
	if err != nil {
		return "", fmt.Errorf("create cloud-init seed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return seed, nil
}

func (k *KVM) createDisk(ctx context.Context, path, image string, sizeGB int) error {
	if image != "" {
		if info, err := os.Stat(image); err == nil && !info.IsDir() {
			output, err := k.run(ctx, "qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", image, path)
			if err != nil {
				return fmt.Errorf("create KVM overlay disk: %w: %s", err, strings.TrimSpace(string(output)))
			}
			baseSize, sizeErr := k.diskVirtualSize(ctx, image)
			requestedSize := int64(sizeGB) * 1024 * 1024 * 1024
			if sizeErr == nil && requestedSize != baseSize {
				resizeArgs := []string{"resize"}
				if requestedSize < baseSize {
					resizeArgs = append(resizeArgs, "--shrink")
				}
				resizeArgs = append(resizeArgs, path, fmt.Sprintf("%dG", sizeGB))
				resizeOutput, resizeErr := k.run(ctx, "qemu-img", resizeArgs...)
				if resizeErr != nil {
					return fmt.Errorf("resize template system disk: %w: %s", resizeErr, strings.TrimSpace(string(resizeOutput)))
				}
			}
			return nil
		}
	}

	return k.createBlankDisk(ctx, path, sizeGB)
}

func (k *KVM) diskVirtualSize(ctx context.Context, path string) (int64, error) {
	output, err := k.run(ctx, "qemu-img", "info", "--output=json", path)
	if err != nil {
		return 0, err
	}
	var info struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal(output, &info); err != nil {
		return 0, err
	}
	return info.VirtualSize, nil
}
func (k *KVM) createBlankDisk(ctx context.Context, path string, sizeGB int) error {
	output, err := k.run(ctx, "qemu-img", "create", "-f", "qcow2", path, fmt.Sprintf("%dG", sizeGB))
	if err != nil {
		return fmt.Errorf("create KVM disk: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func (k *KVM) Status(ctx context.Context, id string) (VM, error) {
	if !kvmNamePattern.MatchString(id) {
		return VM{}, ErrVMNotFound
	}
	output, err := k.virsh(ctx, "dominfo", id)
	if err != nil {
		return VM{}, ErrVMNotFound
	}

	vm := VM{ID: id, Name: id, Status: "stopped", Node: "kvm"}
	vm.Password = k.storedPassword(ctx, id)
	vm.IP = k.dhcpIP(ctx, id)
	if vm.IP == "" {
		vm.IP = k.configuredIP(ctx, id)
	}
	vm.OSType = k.osType(ctx, id)
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "State":
			switch {
			case strings.Contains(value, "running"):
				vm.Status = "running"
			case strings.Contains(value, "paused"):
				vm.Status = "paused"
			default:
				vm.Status = "stopped"
			}
		case "CPU(s)":
			vm.Cores, _ = strconv.Atoi(value)
		case "Max memory":
			fields := strings.Fields(value)
			if len(fields) > 0 {
				kib, _ := strconv.ParseInt(fields[0], 10, 64)
				vm.MemoryMB = int(kib / 1024)
			}
		}
	}
	sizes := k.diskSizes(ctx, id)
	if len(sizes) > 0 {
		vm.SystemDiskGB = sizes[0]
		for _, size := range sizes[1:] {
			vm.DataDiskGB += size
		}
		vm.DiskGB = vm.SystemDiskGB + vm.DataDiskGB
	}

	return vm, nil
}

func (k *KVM) storedPassword(ctx context.Context, id string) string {
	output, err := k.virsh(ctx, "desc", id, "--config")
	if err != nil {
		return ""
	}
	const prefix = "deviy-password:"
	text := string(output)
	start := strings.Index(text, prefix)
	if start < 0 {
		return ""
	}
	value := text[start+len(prefix):]
	if end := strings.IndexAny(value, " \t\r\n"); end >= 0 {
		value = value[:end]
	}
	if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value)); err == nil {
		return string(decoded)
	}

	return ""
}

func (k *KVM) storePassword(ctx context.Context, id, password string) {
	if strings.TrimSpace(password) == "" {
		return
	}
	output, _ := k.virsh(ctx, "desc", id, "--config")
	lines := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "deviy-password:") {
			continue
		}
		lines = append(lines, line)
	}
	lines = append(lines, "deviy-password:"+base64.StdEncoding.EncodeToString([]byte(password)))
	_, _ = k.virsh(ctx, "desc", id, "--config", "--new-desc", strings.Join(lines, "\n"))
}

func (k *KVM) diskSize(ctx context.Context, id string) int {	total := 0
	for _, size := range k.diskSizes(ctx, id) {
		total += size
	}

	return total
}

func (k *KVM) diskSizes(ctx context.Context, id string) []int {
	output, err := k.virsh(ctx, "domblklist", id, "--details")
	if err != nil {
		return nil
	}
	sizes := make([]int, 0)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] == "Type" || fields[0] == "---" || fields[0] == "Target" {
			continue
		}
		if (fields[0] != "file" && fields[0] != "block") || fields[1] != "disk" {
			continue
		}
		infoOutput, infoErr := k.virsh(ctx, "domblkinfo", id, fields[2], "--human")
		if infoErr != nil {
			continue
		}
		for _, infoLine := range strings.Split(string(infoOutput), "\n") {
			key, value, ok := strings.Cut(infoLine, ":")
			if !ok || strings.TrimSpace(key) != "Capacity" {
				continue
			}
			parts := strings.Fields(strings.TrimSpace(value))
			if len(parts) < 2 {
				continue
			}
			size, _ := strconv.ParseFloat(parts[0], 64)
			switch strings.ToUpper(parts[1]) {
			case "GIB", "GB":
				sizes = append(sizes, int(size))
			case "MIB", "MB":
				sizes = append(sizes, int(size/1024))
			case "TIB", "TB":
				sizes = append(sizes, int(size*1024))
			}
			break
		}
	}

	return sizes
}

func (k *KVM) Action(ctx context.Context, id string, action string) error {
	if !kvmNamePattern.MatchString(id) {
		return ErrVMNotFound
	}
	switch action {
	case "start":
		_, err := k.virsh(ctx, "start", id)
		return err
	case "stop", "shutdown":
		_, err := k.virsh(ctx, "shutdown", id)
		return err
	case "force-stop", "destroy":
		_, err := k.virsh(ctx, "destroy", id)
		return err
	case "reboot":
		_, err := k.virsh(ctx, "reboot", id)
		return err
	default:
		return fmt.Errorf("unsupported KVM action: %s", action)
	}
}

func (k *KVM) Delete(ctx context.Context, id string) error {
	if !kvmNamePattern.MatchString(id) {
		return ErrVMNotFound
	}
	_, _ = k.virsh(ctx, "destroy", id)
	diskPaths := make([]string, 0)
	if output, err := k.virsh(ctx, "domblklist", id, "--details"); err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 4 && (fields[0] == "file" || fields[0] == "block") && fields[1] == "disk" {
				diskPaths = append(diskPaths, fields[3])
			}
		}
	}
	output, err := k.virsh(ctx, "undefine", id)
	if err != nil {
		return fmt.Errorf("undefine KVM domain: %w: %s", err, strings.TrimSpace(string(output)))
	}
	for _, path := range diskPaths {
		if filepath.IsAbs(path) {
			_ = os.Remove(path)
		}
	}
	k.removeDHCPHost(ctx, id)

	return nil
}

func (k *KVM) SendKey(ctx context.Context, id string, key string) error {
	if !kvmNamePattern.MatchString(id) {
		return ErrVMNotFound
	}
	switch key {
	case "KEY_ENTER", "KEY_SPACE", "KEY_ESC":
	default:
		return errors.New("unsupported key")
	}
	output, err := k.virsh(ctx, "send-key", id, key)
	if err != nil {
		return fmt.Errorf("send KVM key: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func (k *KVM) CDROM(ctx context.Context, id string) (CDROMInfo, error) {
	if !kvmNamePattern.MatchString(id) {
		return CDROMInfo{}, ErrVMNotFound
	}
	output, err := k.virsh(ctx, "domblklist", id, "--details", "--inactive")
	if err != nil {
		return CDROMInfo{}, fmt.Errorf("read KVM CDROM: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[1] == "cdrom" {
			return CDROMInfo{Mounted: fields[3] != "-", Target: fields[2], ISOFile: fields[3]}, nil
		}
	}

	return CDROMInfo{}, nil
}

func (k *KVM) liveCDROM(ctx context.Context, id string) (CDROMInfo, error) {
	output, err := k.virsh(ctx, "domblklist", id, "--details")
	if err != nil {
		return CDROMInfo{}, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[1] == "cdrom" {
			return CDROMInfo{Mounted: fields[3] != "-", Target: fields[2], ISOFile: fields[3]}, nil
		}
	}

	return CDROMInfo{}, nil
}

func (k *KVM) domainRunning(ctx context.Context, id string) bool {
	output, err := k.virsh(ctx, "domstate", id)
	if err != nil {
		return false
	}
	state := strings.ToLower(strings.TrimSpace(string(output)))

	return state != "shut off" && state != "shutoff"
}

func (k *KVM) MountISO(ctx context.Context, id string, isoFile string) error {
	if !kvmNamePattern.MatchString(id) {
		return ErrVMNotFound
	}
	if !filepath.IsAbs(isoFile) {
		return errors.New("ISO path must be absolute")
	}
	info, err := os.Stat(isoFile)
	if err != nil || info.IsDir() {
		return fmt.Errorf("ISO file not found: %s", isoFile)
	}
	current, err := k.CDROM(ctx, id)
	if err != nil {
		return err
	}
	target := current.Target
	if target == "" {
		used := map[string]bool{}
		if output, listErr := k.virsh(ctx, "domblklist", id, "--details", "--inactive"); listErr == nil {
			for _, line := range strings.Split(string(output), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 3 {
					used[fields[2]] = true
				}
			}
		}
		for _, candidate := range []string{"sdb", "sdc", "sdd", "sde", "hdb", "hdc"} {
			if !used[candidate] {
				target = candidate
				break
			}
		}
	}
	if target == "" {
		return errors.New("no free CDROM target")
	}
	bus := "sata"
	if strings.HasPrefix(target, "h") {
		bus = "ide"
	}
	xmlData, err := k.virsh(ctx, "dumpxml", id, "--inactive")
	if err != nil {
		return fmt.Errorf("read KVM domain XML: %w", err)
	}
	xmlText := removeCDROMDisks(string(xmlData))
	cdromXML := fmt.Sprintf(`<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='%s'/><target dev='%s' bus='%s'/><readonly/></disk>`, xmlEscape(isoFile), xmlEscape(target), bus)
	xmlText = strings.Replace(xmlText, "</devices>", cdromXML+"\n</devices>", 1)
	if !strings.Contains(xmlText, "</devices>") {
		return errors.New("invalid KVM domain XML")
	}
	if err := k.defineXML(ctx, xmlText); err != nil {
		return err
	}
	if k.domainRunning(ctx, id) {
		live, liveErr := k.liveCDROM(ctx, id)
		if liveErr != nil {
			return liveErr
		}
		if live.Target != "" {
			output, changeErr := k.virsh(ctx, "change-media", id, live.Target, isoFile, "--update", "--live")
			if changeErr != nil {
				return fmt.Errorf("insert live CDROM: %w: %s", changeErr, strings.TrimSpace(string(output)))
			}
		} else {
			output, attachErr := k.virsh(ctx, "attach-disk", id, isoFile, target, "--type", "cdrom", "--mode", "readonly", "--targetbus", bus, "--live")
			if attachErr != nil {
				return fmt.Errorf("attach live CDROM: %w: %s", attachErr, strings.TrimSpace(string(output)))
			}
		}
	}

	return nil
}

func (k *KVM) EjectISO(ctx context.Context, id string) error {
	if !kvmNamePattern.MatchString(id) {
		return ErrVMNotFound
	}
	if k.domainRunning(ctx, id) {
		live, liveErr := k.liveCDROM(ctx, id)
		if liveErr != nil {
			return liveErr
		}
		if live.Target != "" {
			output, ejectErr := k.virsh(ctx, "change-media", id, live.Target, "--eject", "--live")
			if ejectErr != nil {
				return fmt.Errorf("eject live CDROM: %w: %s", ejectErr, strings.TrimSpace(string(output)))
			}
		}
	}
	xmlData, err := k.virsh(ctx, "dumpxml", id, "--inactive")
	if err != nil {
		return fmt.Errorf("read KVM domain XML: %w", err)
	}
	xmlText := removeCDROMDisks(string(xmlData))
	if xmlText == string(xmlData) {
		return nil
	}

	return k.defineXML(ctx, xmlText)
}

func (k *KVM) DeleteTemplate(ctx context.Context, target string) error {
	isoDir := k.cfg.ISODir
	if isoDir == "" {
		isoDir = "/var/lib/libvirt/images/iso"
	}
	templateDir := filepath.Join(filepath.Dir(isoDir), "templates")
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("template path is required")
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	absDir, err := filepath.Abs(templateDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absDir, absTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("template path is outside template directory")
	}
	if _, err := os.Stat(absTarget); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	inUse, err := k.templateInUse(ctx, absTarget)
	if err != nil {
		return err
	}
	if inUse {
		return ErrTemplateInUse
	}
	return os.Remove(absTarget)
}
func (k *KVM) templateInUse(ctx context.Context, target string) (bool, error) {
	output, err := k.virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return false, err
	}
	for _, name := range strings.Fields(string(output)) {
		disks, err := k.virsh(ctx, "domblklist", name, "--details")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(disks), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[3] == "-" {
				continue
			}
			source := fields[3]
			if source == target {
				return true, nil
			}
			info, infoErr := k.run(ctx, "qemu-img", "info", "--backing-chain", source)
			if infoErr == nil && strings.Contains(string(info), target) {
				return true, nil
			}
		}
	}
	return false, nil
}
func (k *KVM) CreateTemplate(ctx context.Context, id string, name string) (TemplateInfo, error) {
	if !kvmNamePattern.MatchString(id) {
		return TemplateInfo{}, ErrVMNotFound
	}
	vm, err := k.Status(ctx, id)
	if err != nil {
		return TemplateInfo{}, err
	}
	if vm.Status != "stopped" {
		return TemplateInfo{}, errors.New("虚拟机必须先关机，才能转换为模板")
	}
	name = strings.TrimSpace(strings.TrimSuffix(name, ".qcow2"))
	if name == "" {
		name = id + "-template"
	}
	name = regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-_")
	if name == "" || !kvmNamePattern.MatchString(name) {
		return TemplateInfo{}, errors.New("模板名称不合法")
	}
	isoDir := k.cfg.ISODir
	if isoDir == "" {
		isoDir = "/var/lib/libvirt/images/iso"
	}
	templateDir := filepath.Join(filepath.Dir(isoDir), "templates")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		return TemplateInfo{}, fmt.Errorf("创建模板目录失败: %w", err)
	}
	target := filepath.Join(templateDir, name+".qcow2")
	if _, err := os.Stat(target); err == nil {
		inUse, checkErr := k.templateInUse(ctx, target)
		if checkErr != nil {
			return TemplateInfo{}, fmt.Errorf("检查模板占用失败: %w", checkErr)
		}
		if inUse {
			return TemplateInfo{}, fmt.Errorf("同名模板已存在且正在被虚拟机使用，请更换模板名称或先删除使用它的虚拟机: %s", target)
		}
		if err := os.Remove(target); err != nil {
			return TemplateInfo{}, fmt.Errorf("覆盖旧模板文件失败: %w", err)
		}
	}
	output, err := k.virsh(ctx, "domblklist", id, "--details")
	if err != nil {
		return TemplateInfo{}, fmt.Errorf("读取虚拟机磁盘失败: %w", err)
	}
	source := ""
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[1] == "disk" && fields[3] != "-" {
			source = fields[3]
			break
		}
	}
	if source == "" {
		return TemplateInfo{}, errors.New("虚拟机没有系统盘")
	}
	temp := target + ".partial"
	_ = os.Remove(temp)
	convertOutput, convertErr := k.run(ctx, "qemu-img", "convert", "-O", "qcow2", source, temp)
	if convertErr != nil {
		_ = os.Remove(temp)
		return TemplateInfo{}, fmt.Errorf("转换模板失败: %w: %s", convertErr, strings.TrimSpace(string(convertOutput)))
	}
	if err := os.Chmod(temp, 0o644); err != nil {
		_ = os.Remove(temp)
		return TemplateInfo{}, err
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return TemplateInfo{}, err
	}
	info, _ := os.Stat(target)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	return TemplateInfo{Name: name, Path: target, OSType: vm.OSType, SizeBytes: size}, nil
}
func removeCDROMDisks(xmlText string) string {
	pattern := regexp.MustCompile(`(?s)<disk\b[^>]*device=["']cdrom["'][^>]*>.*?</disk>`)
	return pattern.ReplaceAllString(xmlText, "")
}

func xmlEscape(value string) string {
	var builder strings.Builder
	_ = xml.EscapeText(&builder, []byte(value))

	return builder.String()
}

func (k *KVM) defineXML(ctx context.Context, xmlText string) error {
	path := filepath.Join(os.TempDir(), "deviy-domain.xml")
	if err := os.WriteFile(path, []byte(xmlText), 0o600); err != nil {
		return err
	}
	defer os.Remove(path)
	output, err := k.virsh(ctx, "define", path)
	if err != nil {
		return fmt.Errorf("define KVM domain: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func (k *KVM) SetBootOrder(ctx context.Context, id string, order []string, isoFile string) error {
	if !kvmNamePattern.MatchString(id) {
		return ErrVMNotFound
	}
	if len(order) == 0 || len(order) > 3 {
		return errors.New("boot order must contain 1-3 devices")
	}
	seen := map[string]bool{}
	for _, device := range order {
		switch device {
		case "hd", "cdrom", "network":
		default:
			return errors.New("unsupported boot device")
		}
		if seen[device] {
			return errors.New("duplicate boot device")
		}
		seen[device] = true
	}
	if isoFile != "" {
		if err := k.MountISO(ctx, id, isoFile); err != nil {
			return err
		}
	}
	xmlData, err := k.virsh(ctx, "dumpxml", id, "--inactive")
	if err != nil {
		return fmt.Errorf("read KVM domain XML: %w", err)
	}
	cleaned := regexp.MustCompile(`(?m)\s*<boot\s+order=["'][^"']*["']\s*/>`).ReplaceAllString(string(xmlData), "")
	if cleaned != string(xmlData) {
		if err := k.defineXML(ctx, cleaned); err != nil {
			return err
		}
	}
	output, err := k.run(ctx, "virt-xml", id, "--boot", strings.Join(order, ","), "--edit")
	if err != nil {
		return fmt.Errorf("set KVM boot order: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func (k *KVM) Reinstall(ctx context.Context, id string, image string, isoFile string, ostype string) (VM, error) {
	vm, err := k.Status(ctx, id)
	if err != nil {
		return VM{}, err
	}
	if err := k.Delete(ctx, id); err != nil {
		return VM{}, err
	}
	if ostype == "" {
		ostype = "win11"
	}

	return k.Create(ctx, CreateVMInput{
		Name:        id,
		Cores:       vm.Cores,
		MemoryMB:    vm.MemoryMB,
		DiskGB:      vm.SystemDiskGB,
		DataDiskGB:  vm.DataDiskGB,
		OSType:      ostype,
		Image:       image,
		ISOFile:     isoFile,
		IP:          vm.IP,
		NetworkMode: "private",
		Start:       true,
	})
}

func (k *KVM) Stats(ctx context.Context, id string) (VMStats, error) {
	if !kvmNamePattern.MatchString(id) {
		return VMStats{}, ErrVMNotFound
	}
	// Enable periodic balloon statistics so domstats can report guest free memory.
	_, _ = k.virsh(ctx, "dommemstat", id, "--period", "1")
	output, err := k.virsh(ctx, "domstats", id, "--state", "--cpu-total", "--balloon", "--interface", "--block")
	if err != nil {
		return VMStats{}, fmt.Errorf("read KVM stats: %w: %s", err, strings.TrimSpace(string(output)))
	}
	stats := VMStats{}
	var balloonCurrent, balloonUnused, balloonUsable uint64
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		number, _ := strconv.ParseUint(value, 10, 64)
		switch {
		case key == "cpu.time":
			stats.CPUTimeNS = number
		case key == "balloon.current":
			balloonCurrent = number
			stats.MemoryMB = int(number / 1024)
			stats.MemoryTotalMB = stats.MemoryMB
		case key == "balloon.maximum":
			if stats.MemoryTotalMB == 0 {
				stats.MemoryTotalMB = int(number / 1024)
			}
		case key == "balloon.available":
			stats.MemoryAvailableMB = int(number / 1024)
		case key == "balloon.unused":
			balloonUnused = number
		case key == "balloon.usable":
			balloonUsable = number
		case key == "balloon.rss":
			stats.MemoryUsedMB = int(number / 1024)
		case strings.HasPrefix(key, "net.") && strings.HasSuffix(key, ".rx.bytes"):
			stats.NetRxBytes += number
		case strings.HasPrefix(key, "net.") && strings.HasSuffix(key, ".tx.bytes"):
			stats.NetTxBytes += number
		case strings.HasPrefix(key, "block.") && strings.HasSuffix(key, ".rd.bytes"):
			stats.DiskRDBytes += number
		case strings.HasPrefix(key, "block.") && strings.HasSuffix(key, ".wr.bytes"):
			stats.DiskWRBytes += number
		}
	}

	if stats.MemoryTotalMB == 0 {
		stats.MemoryTotalMB = stats.MemoryMB
	}
	if balloonCurrent > 0 && balloonUnused > 0 && balloonCurrent > balloonUnused {
		stats.MemoryUsedMB = int((balloonCurrent - balloonUnused) / 1024)
	} else if balloonCurrent > 0 && balloonUsable > 0 && balloonCurrent > balloonUsable {
		stats.MemoryUsedMB = int((balloonCurrent - balloonUsable) / 1024)
	}
	if stats.MemoryUsedMB == 0 && stats.MemoryAvailableMB > 0 && stats.MemoryTotalMB > stats.MemoryAvailableMB {
		stats.MemoryUsedMB = stats.MemoryTotalMB - stats.MemoryAvailableMB
	}

	return stats, nil
}

func (k *KVM) SetBandwidth(ctx context.Context, id string, uploadMbps int, downloadMbps int) error {
	if !kvmNamePattern.MatchString(id) {
		return ErrVMNotFound
	}
	if uploadMbps < 0 || downloadMbps < 0 || uploadMbps > 100000 || downloadMbps > 100000 {
		return errors.New("bandwidth must be between 0 and 100000 Mbps")
	}
	output, err := k.virsh(ctx, "domiflist", id)
	if err != nil {
		return fmt.Errorf("read KVM interfaces: %w", err)
	}
	target := ""
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 && strings.Contains(fields[4], ":") {
			target = fields[4]
			if fields[3] == "e1000e" || fields[3] == "e1000" {
				break
			}
		}
	}
	if target == "" {
		return errors.New("KVM VM has no active network interface")
	}
	args := []string{"domiftune", id, target, "--live", "--config"}
	args = append(args, "--inbound", strconv.Itoa(downloadMbps*125), "--outbound", strconv.Itoa(uploadMbps*125))
	output, err = k.virsh(ctx, args...)
	if len(args) == 5 {
		return errors.New("no bandwidth value supplied")
	}
	output, err = k.virsh(ctx, args...)
	if err != nil {
		return fmt.Errorf("set KVM bandwidth: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func (k *KVM) osType(ctx context.Context, id string) string {
	output, _ := k.virsh(ctx, "desc", id, "--config")
	text := strings.ToLower(strings.TrimSpace(string(output)))
	if marker := strings.Index(text, "deviy:ostype="); marker >= 0 {
		value := text[marker+len("deviy:ostype="):]
		if fields := strings.Fields(value); len(fields) > 0 {
			return strings.Trim(fields[0], "\r\n")
		}
	}
	if strings.Contains(strings.ToLower(id), "win") {
		return "win"
	}

	return "l26"
}

func (k *KVM) configuredIP(ctx context.Context, id string) string {
	output, err := k.virsh(ctx, "dumpxml", id, "--inactive")
	if err != nil {
		return ""
	}
	match := regexp.MustCompile(`DV1;([0-9.]+);`).FindStringSubmatch(string(output))
	if len(match) != 2 || net.ParseIP(match[1]) == nil {
		return ""
	}
	return match[1]
}
func (k *KVM) dhcpIP(ctx context.Context, id string) string {
	ifaceOutput, err := k.virsh(ctx, "domiflist", id)
	if err != nil {
		return ""
	}
	mac := ""
	for _, line := range strings.Split(string(ifaceOutput), "\n") {
		if match := regexp.MustCompile(`([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}`).FindString(line); match != "" {
			mac = match
			break
		}
	}
	if mac == "" {
		return ""
	}
	leaseOutput, err := k.virsh(ctx, "net-dhcp-leases", k.cfg.Network)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(leaseOutput), "\n") {
		if !strings.Contains(line, mac) {
			continue
		}
		fields := strings.Fields(line)
		for _, field := range fields {
			candidate := strings.TrimSuffix(field, ",")
			if slash := strings.IndexByte(candidate, '/'); slash >= 0 {
				candidate = candidate[:slash]
			}
			if ip := net.ParseIP(candidate); ip != nil && ip.To4() != nil {
				return ip.String()
			}
		}
	}
	if neighborOutput, neighborErr := k.run(ctx, "ip", "neigh", "show"); neighborErr == nil {
		for _, line := range strings.Split(string(neighborOutput), "\n") {
			if !strings.Contains(line, mac) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) > 0 {
				if ip := net.ParseIP(fields[0]); ip != nil && ip.To4() != nil {
					return ip.String()
				}
			}
		}
	}

	return ""
}

func (k *KVM) VNC(ctx context.Context, id string) (VNCInfo, error) {
	if !kvmNamePattern.MatchString(id) {
		return VNCInfo{}, ErrVMNotFound
	}
	output, err := k.virsh(ctx, "vncdisplay", id)
	if err != nil {
		return VNCInfo{}, fmt.Errorf("read KVM VNC display: %w: %s", err, strings.TrimSpace(string(output)))
	}
	display := strings.TrimSpace(string(output))
	if display == "" {
		return VNCInfo{}, errors.New("KVM VNC is not enabled for this VM")
	}
	index := 0
	if colon := strings.LastIndex(display, ":"); colon >= 0 {
		index, _ = strconv.Atoi(display[colon+1:])
	}

	return VNCInfo{Host: "127.0.0.1", Port: 5900 + index, Display: display}, nil
}

func (k *KVM) Backup(ctx context.Context, id string, destination string) (string, error) {
	if !kvmNamePattern.MatchString(id) {
		return "", ErrVMNotFound
	}
	if destination == "" {
		destination = k.cfg.BackupDir
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return "", err
	}
	output, err := k.virsh(ctx, "domblklist", id, "--details")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || (fields[0] != "file" && fields[0] != "block") {
			continue
		}
		target := filepath.Join(destination, id+".qcow2")
		convertOutput, convertErr := k.run(ctx, "qemu-img", "convert", "-O", "qcow2", fields[3], target)
		if convertErr != nil {
			return "", fmt.Errorf("KVM backup failed: %w: %s", convertErr, strings.TrimSpace(string(convertOutput)))
		}
		return target, nil
	}

	return "", errors.New("no KVM disk found")
}
