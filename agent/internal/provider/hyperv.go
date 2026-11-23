package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type PowerShellRunner interface {
	Run(ctx context.Context, script string) ([]byte, error)
}

type SystemPowerShell struct{}

func (SystemPowerShell) Run(ctx context.Context, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = strings.NewReader(script)

	return cmd.CombinedOutput()
}

type HyperVConfig struct {
	SwitchName string
	VMPath     string
	Storage    string
}

type HyperV struct {
	cfg    HyperVConfig
	runner PowerShellRunner
}

var hyperVGuidPattern = regexp.MustCompile(`^[0-9a-fA-F-]{32,36}$`)

func NewHyperV(cfg HyperVConfig, runner PowerShellRunner) (*HyperV, error) {
	if cfg.SwitchName == "" {
		cfg.SwitchName = "Default Switch"
	}
	if cfg.Storage == "" {
		cfg.Storage = `C:\ProgramData\Deviy\VHDs`
	}
	if runner == nil {
		runner = SystemPowerShell{}
	}

	return &HyperV{cfg: cfg, runner: runner}, nil
}

func (h *HyperV) Name() string {
	return "hyperv"
}

func (h *HyperV) Diagnose(ctx context.Context) []Diagnostic {
	checks := []Diagnostic{}

	adminScript := `$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent()); if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { 'yes' } else { throw 'not administrator' }`
	if _, err := h.run(ctx, adminScript); err != nil {
		checks = append(checks, Diagnostic{Name: "hyperv.admin", Status: "failed", Message: "需要管理员权限：" + err.Error()})
	} else {
		checks = append(checks, Diagnostic{Name: "hyperv.admin", Status: "ok", Message: "PowerShell 管理员权限正常"})
	}

	vmScript := `$count = @(Get-VM -ErrorAction Stop).Count; 'count=' + $count`
	if output, err := h.run(ctx, vmScript); err != nil {
		checks = append(checks, Diagnostic{Name: "hyperv.service", Status: "failed", Message: "Hyper-V 服务/Get-VM 不可用：" + err.Error()})
	} else {
		checks = append(checks, Diagnostic{Name: "hyperv.service", Status: "ok", Message: "Hyper-V 服务可用：" + strings.TrimSpace(string(output))})
	}

	switchScript := fmt.Sprintf(`$s = Get-VMSwitch -Name '%s' -ErrorAction Stop; 'switch=' + $s.Name`, quotePowerShell(h.cfg.SwitchName))
	if output, err := h.run(ctx, switchScript); err != nil {
		checks = append(checks, Diagnostic{Name: "hyperv.switch", Status: "failed", Message: "虚拟交换机不存在：" + h.cfg.SwitchName + "：" + err.Error()})
	} else {
		checks = append(checks, Diagnostic{Name: "hyperv.switch", Status: "ok", Message: "虚拟交换机存在：" + strings.TrimSpace(string(output))})
	}

	storageScript := fmt.Sprintf(`if (Test-Path -LiteralPath '%s') { 'ok' } else { throw 'path missing' }`, quotePowerShell(h.cfg.Storage))
	if _, err := h.run(ctx, storageScript); err != nil {
		checks = append(checks, Diagnostic{Name: "hyperv.storage", Status: "warning", Message: "VHDX 目录不存在，创建 VM 时会自动创建：" + h.cfg.Storage})
	} else {
		checks = append(checks, Diagnostic{Name: "hyperv.storage", Status: "ok", Message: "VHDX 目录存在：" + h.cfg.Storage})
	}

	return checks
}

func (h *HyperV) List(ctx context.Context) ([]VM, error) {
	script := `
$ErrorActionPreference = 'Stop'
$items = Get-VM | ForEach-Object {
    $vm = $_
    $disks = @(Get-VMHardDiskDrive -VM $vm -ErrorAction SilentlyContinue)
    $systemDiskGB = 0
    $dataDiskGB = 0
    for ($i = 0; $i -lt $disks.Count; $i++) {
        $vhd = Get-VHD -Path $disks[$i].Path -ErrorAction SilentlyContinue
        if ($vhd) {
            $size = [int][math]::Round($vhd.Size / 1GB)
            if ($i -eq 0) { $systemDiskGB = $size } else { $dataDiskGB += $size }
        }
    }
    $status = 'paused'
    if ($vm.State -eq 'Running') { $status = 'running' }
    if ($vm.State -eq 'Off') { $status = 'stopped' }
    [pscustomobject]@{
        id = $vm.Id.ToString()
        name = $vm.Name
        status = $status
        cores = [int]$vm.ProcessorCount
        memory_mb = [int]($vm.MemoryAssigned / 1MB)
        disk_gb = $systemDiskGB + $dataDiskGB
        system_disk_gb = $systemDiskGB
        data_disk_gb = $dataDiskGB
        node = $env:COMPUTERNAME
        uptime = if ($vm.Uptime) { [int]$vm.Uptime.TotalSeconds } else { 0 }
    }
}
if ($items) { $items | ConvertTo-Json -Compress -Depth 4 } else { '[]' }
`
	output, err := h.run(ctx, script)
	if err != nil {
		return nil, err
	}

	return decodeHyperVVMs(output)
}

func (h *HyperV) Create(ctx context.Context, input CreateVMInput) (VM, error) {
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
		input.Storage = h.cfg.Storage
	}

	name := sanitizeHyperVName(input.Name)
	memoryBytes := int64(input.MemoryMB) * 1024 * 1024
	diskBytes := int64(input.DiskGB) * 1024 * 1024 * 1024
	dataDiskBytes := int64(input.DataDiskGB) * 1024 * 1024 * 1024
	start := "$false"
	if input.Start {
		start = "$true"
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$name = '%s'
$switch = '%s'
$dir = '%s'
$image = '%s'
$isoFile = '%s'
$templateDir = '%s'
$dataDiskDir = '%s'
$isoDir = '%s'
$backupDir = '%s'
if (!(Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
foreach ($sub in @($templateDir, $dataDiskDir, $isoDir, $backupDir)) {
    if ($sub) {
        $path = $sub
        if (![System.IO.Path]::IsPathRooted($path)) { $path = Join-Path $dir $path }
        if (!(Test-Path -LiteralPath $path)) { New-Item -ItemType Directory -Force -Path $path | Out-Null }
    }
}
$dataPath = $dataDiskDir
if (!$dataPath) { $dataPath = $dir }
if (![System.IO.Path]::IsPathRooted($dataPath)) { $dataPath = Join-Path $dir $dataPath }
$vhd = Join-Path $dir ($name + '.vhdx')
if (Get-VM -Name $name -ErrorAction SilentlyContinue) { throw "VM 已存在: $name" }
if ($image) {
    if (!(Test-Path -LiteralPath $image)) { throw "镜像不存在: $image" }
    Copy-Item -LiteralPath $image -Destination $vhd -Force
    New-VM -Name $name -MemoryStartupBytes %d -VHDPath $vhd -SwitchName $switch -Generation 2 | Out-Null
} else {
    New-VM -Name $name -MemoryStartupBytes %d -NewVHDPath $vhd -NewVHDSizeBytes %d -SwitchName $switch -Generation 2 | Out-Null
}
Set-VMProcessor -VMName $name -Count %d
Set-VMMemory -VMName $name -StartupBytes %d -DynamicMemoryEnabled $true
if ($isoFile) {
    if (!(Test-Path -LiteralPath $isoFile)) { throw "ISO 不存在: $isoFile" }
    Add-VMDvdDrive -VMName $name -Path $isoFile | Out-Null
}
if (%d -gt 0) {
    $dataVhd = Join-Path $dataPath ($name + '-data.vhdx')
    New-VHD -Path $dataVhd -SizeBytes %d -Dynamic | Out-Null
    Add-VMHardDiskDrive -VMName $name -Path $dataVhd | Out-Null
}
if (%s) { Start-VM -Name $name | Out-Null }
(Get-VM -Name $name).Id.ToString()
`, quotePowerShell(name), quotePowerShell(h.cfg.SwitchName), quotePowerShell(input.Storage), quotePowerShell(input.Image), quotePowerShell(input.ISOFile), quotePowerShell(input.TemplateDir), quotePowerShell(input.DataDiskDir), quotePowerShell(input.ISODir), quotePowerShell(input.BackupDir), memoryBytes, memoryBytes, diskBytes, input.Cores, memoryBytes, input.DataDiskGB, dataDiskBytes, start)

	output, err := h.run(ctx, script)
	if err != nil {
		return VM{}, err
	}

	id := strings.TrimSpace(string(output))
	if !hyperVGuidPattern.MatchString(id) {
		return VM{}, fmt.Errorf("Hyper-V 未返回有效 VM ID: %q", id)
	}

	return h.Status(ctx, id)
}

func (h *HyperV) Backup(ctx context.Context, id string, destination string) (string, error) {
	if !hyperVGuidPattern.MatchString(id) {
		return "", ErrVMNotFound
	}
	if destination == "" {
		return "", errors.New("backup directory is required")
	}
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$destination = '%s'
if (!(Test-Path -LiteralPath $destination)) { New-Item -ItemType Directory -Force -Path $destination | Out-Null }
$vm = Get-VM -Id '%s'
Export-VM -Name $vm.Name -Path $destination | Out-Null
$destination
`, quotePowerShell(destination), quotePowerShell(id))

	output, err := h.run(ctx, script)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

func (h *HyperV) Status(ctx context.Context, id string) (VM, error) {
	if !hyperVGuidPattern.MatchString(id) {
		return VM{}, ErrVMNotFound
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Id '%s'
$disks = @(Get-VMHardDiskDrive -VM $vm -ErrorAction SilentlyContinue)
$systemDiskGB = 0
$dataDiskGB = 0
for ($i = 0; $i -lt $disks.Count; $i++) {
    $vhd = Get-VHD -Path $disks[$i].Path -ErrorAction SilentlyContinue
    if ($vhd) {
        $size = [int][math]::Round($vhd.Size / 1GB)
        if ($i -eq 0) { $systemDiskGB = $size } else { $dataDiskGB += $size }
    }
}
$status = 'paused'
if ($vm.State -eq 'Running') { $status = 'running' }
if ($vm.State -eq 'Off') { $status = 'stopped' }
[pscustomobject]@{
    id = $vm.Id.ToString()
    name = $vm.Name
    status = $status
    cores = [int]$vm.ProcessorCount
    memory_mb = [int]($vm.MemoryAssigned / 1MB)
    disk_gb = $systemDiskGB + $dataDiskGB
    system_disk_gb = $systemDiskGB
    data_disk_gb = $dataDiskGB
    node = $env:COMPUTERNAME
    uptime = if ($vm.Uptime) { [int]$vm.Uptime.TotalSeconds } else { 0 }
} | ConvertTo-Json -Compress -Depth 4
`, quotePowerShell(id))

	output, err := h.run(ctx, script)
	if err != nil {
		return VM{}, ErrVMNotFound
	}

	var vm VM
	if err := json.Unmarshal(output, &vm); err != nil {
		return VM{}, err
	}

	return vm, nil
}

func (h *HyperV) Action(ctx context.Context, id string, action string) error {
	if !hyperVGuidPattern.MatchString(id) {
		return ErrVMNotFound
	}

	var command string
	switch action {
	case "start":
		command = "Start-VM"
	case "stop", "shutdown":
		command = "Stop-VM"
	case "reboot":
		command = "Restart-VM"
	default:
		return errors.New("unsupported action")
	}

	force := ""
	if action == "stop" || action == "shutdown" || action == "reboot" {
		force = " -Force"
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
%s -Id '%s'%s | Out-Null
`, command, quotePowerShell(id), force)

	_, err := h.run(ctx, script)

	return err
}

func (h *HyperV) Delete(ctx context.Context, id string) error {
	if !hyperVGuidPattern.MatchString(id) {
		return ErrVMNotFound
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Id '%s'
if ($vm.State -ne 'Off') { Stop-VM -Id $vm.Id -TurnOff -Force -ErrorAction SilentlyContinue }
Remove-VM -Id $vm.Id -Force
`, quotePowerShell(id))

	_, err := h.run(ctx, script)

	return err
}

func (h *HyperV) run(ctx context.Context, script string) ([]byte, error) {
	output, err := h.runner.Run(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("PowerShell 执行失败: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return output, nil
}

func decodeHyperVVMs(output []byte) ([]VM, error) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" {
		return []VM{}, nil
	}

	var vms []VM
	if err := json.Unmarshal([]byte(trimmed), &vms); err == nil {
		return vms, nil
	}

	var single VM
	if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
		return nil, err
	}

	return []VM{single}, nil
}

func sanitizeHyperVName(name string) string {
	replacer := regexp.MustCompile(`[^A-Za-z0-9_-]`)
	safe := strings.Trim(replacer.ReplaceAllString(name, "-"), "-")
	if safe == "" {
		safe = "deviy-vm-" + strconv.FormatInt(int64(len(name)), 10)
	}

	return safe
}

func quotePowerShell(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
