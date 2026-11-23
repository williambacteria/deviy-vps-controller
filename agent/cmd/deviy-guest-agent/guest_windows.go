//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func applyPassword(username, password string) error {
	script := fmt.Sprintf(
		"$ErrorActionPreference='Stop'; $p=ConvertTo-SecureString '%s' -AsPlainText -Force; Enable-LocalUser -Name '%s' -ErrorAction SilentlyContinue; Set-LocalUser -Name '%s' -Password $p -PasswordNeverExpires $true; net user '%s' '%s' | Out-Null",
		escapePowerShell(password), username, username, username, escapePowerShell(password),
	)
	if output, err := runPowerShell(script); err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	return nil
}

func configureNetwork(ip string, prefix int, gateway string, dns1 string, dns2 string) error {
	dnsServers := strings.Trim(strings.Join([]string{strings.TrimSpace(dns1), strings.TrimSpace(dns2)}, ","), ",")
	if dnsServers == "" {
		dnsServers = gateway
	}
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'
$adapter=Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object {$_.Status -eq 'Up'} | Select-Object -First 1
if(-not $adapter){ throw '没有可用的网络适配器' }
Set-NetIPInterface -InterfaceIndex $adapter.ifIndex -Dhcp Disabled
Get-NetIPAddress -InterfaceIndex $adapter.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue
New-NetIPAddress -InterfaceIndex $adapter.ifIndex -IPAddress '%s' -PrefixLength %d -DefaultGateway '%s' | Out-Null
Set-DnsClientServerAddress -InterfaceIndex $adapter.ifIndex -ServerAddresses '%s'
`, ip, prefix, gateway, dnsServers)
	if output, err := runPowerShell(script); err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	return nil
}

func applyDNS(dns1 string, dns2 string) error {
	dnsServers := strings.Trim(strings.Join([]string{strings.TrimSpace(dns1), strings.TrimSpace(dns2)}, ","), ",")
	if dnsServers == "" {
		return errors.New("dns is required")
	}
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'
$adapter=Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object {$_.Status -eq 'Up'} | Select-Object -First 1
if(-not $adapter){ throw '没有可用的网络适配器' }
Set-DnsClientServerAddress -InterfaceIndex $adapter.ifIndex -ServerAddresses '%s'
`, dnsServers)
	if output, err := runPowerShell(script); err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	return nil
}

func resizeSystemDisk() (string, error) {
	script := `$ErrorActionPreference='Stop'
$c=Get-Partition -DriveLetter C -ErrorAction Stop
$disk=Get-Disk -Number $c.DiskNumber -ErrorAction Stop
$supported=Get-PartitionSupportedSize -DriveLetter C -ErrorAction Stop
if($supported.SizeMax -gt $c.Size){
  Resize-Partition -DriveLetter C -Size $supported.SizeMax -ErrorAction Stop
  "resized system partition to $($supported.SizeMax) bytes"
  return
}
$after=@(Get-Partition -DiskNumber $disk.Number -ErrorAction SilentlyContinue | Where-Object {$_.Offset -gt $c.Offset} | Sort-Object Offset)
if($after.Count -eq 0){ throw "C 盘后方没有可合并的恢复分区或未分配空间" }
$isRecovery={param($p) $letter=([string]$p.DriveLetter).Trim([char]0).Trim(); $p.GptType -eq '{de94bba4-06d1-4d40-a16a-bfd50179d6ac}' -or $p.Type -eq 'Recovery' -or ([string]::IsNullOrWhiteSpace($letter) -and $p.Size -lt 4GB -and $p.Size -gt 100MB)}
$recovery=@($after | Where-Object { & $isRecovery $_ })
$other=@($after | Where-Object { -not (& $isRecovery $_) })
if($recovery.Count -eq 0 -or $other.Count -gt 0){
  $detail=($after | ForEach-Object { "num=$($_.PartitionNumber) type=$($_.Type) gpt=$($_.GptType) size=$($_.Size) letter=$($_.DriveLetter)" }) -join "; "
  throw "C 盘后方存在非恢复分区，已拒绝自动扩展: $detail"
}
$lastEnd=($after | ForEach-Object {$_.Offset + $_.Size} | Measure-Object -Maximum).Maximum
$freeBytes=$disk.Size - $lastEnd
if($freeBytes -lt 1GB){
  throw "恢复分区后方没有未分配空间"
}
& reagentc.exe /disable | Out-Null
foreach($part in $recovery){
  try {
    Remove-Partition -DiskNumber $disk.Number -PartitionNumber $part.PartitionNumber -Confirm:$false -ErrorAction Stop | Out-Null
  } catch {
    $lines=@("select disk $($disk.Number)","select partition $($part.PartitionNumber)","delete partition override")
    $tmp=Join-Path $env:TEMP "deviy-resize.txt"
    Set-Content -LiteralPath $tmp -Value $lines -Encoding ASCII
    & diskpart.exe /s $tmp | Out-Null
    Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
  }
}
$supported=Get-PartitionSupportedSize -DriveLetter C -ErrorAction Stop
Resize-Partition -DriveLetter C -Size $supported.SizeMax -ErrorAction Stop
$newSize=(Get-Partition -DriveLetter C).Size
if($newSize -le $c.Size){ throw "C 盘扩展后大小没有增加" }
try { & reagentc.exe /enable | Out-Null } catch {}
"resized C from $($c.Size) to $newSize bytes"
`
	output, err := runPowerShell(script)
	if err != nil {
		return "", errors.New(strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func ensureDataDisks() (string, error) {
	script := `$ErrorActionPreference='Stop'
$sys=Get-Disk | Where-Object {$_.IsSystem} | Select-Object -First 1
$result=@()
foreach($disk in (Get-Disk | Where-Object {-not $_.IsSystem -and $_.Size -gt 1GB})){
  if($disk.IsOffline){ Set-Disk -Number $disk.Number -IsOffline $false }
  if($disk.IsReadOnly){ Set-Disk -Number $disk.Number -IsReadOnly $false }
  if($disk.PartitionStyle -eq 'RAW'){ Initialize-Disk -Number $disk.Number -PartitionStyle GPT -Confirm:$false | Out-Null }
  $part=Get-Partition -DiskNumber $disk.Number -ErrorAction SilentlyContinue | Where-Object {$_.Type -eq 'Basic'} | Select-Object -First 1
  if(-not $part){ $part=New-Partition -DiskNumber $disk.Number -UseMaximumSize -AssignDriveLetter }
  $vol=Get-Volume -Partition $part -ErrorAction SilentlyContinue
  if(-not $vol.DriveLetter){ Add-PartitionAccessPath -DiskNumber $disk.Number -PartitionNumber $part.PartitionNumber -AssignDriveLetter -ErrorAction SilentlyContinue }
  $vol=Get-Volume -Partition $part -ErrorAction SilentlyContinue
  if($vol.FileSystem -ne 'NTFS'){ Format-Volume -Partition $part -FileSystem NTFS -NewFileSystemLabel 'DeviyData' -Confirm:$false | Out-Null }
  $vol=Get-Volume -Partition $part -ErrorAction SilentlyContinue
  $result += "disk=$($disk.Number) drive=$($vol.DriveLetter) size_gb=$([math]::Round($disk.Size/1GB,2))"
}
$result -join [Environment]::NewLine`
	output, err := runPowerShell(script)
	if err != nil {
		return "", errors.New(strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func rebootSystem() error {
	if output, err := exec.Command("shutdown.exe", "/r", "/t", "0", "/f").CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	return nil
}

func setHostname(name string) error {
	script := fmt.Sprintf("Rename-Computer -NewName '%s' -Force", name)
	if output, err := runPowerShell(script); err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	return nil
}

func bootstrapFromSMBIOS() {
	serialOutput, err := runPowerShell("(Get-CimInstance Win32_ComputerSystem).SerialNumber")
	if err != nil {
		return
	}
	serial := strings.TrimSpace(string(serialOutput))
	parts := strings.Split(serial, ";")
	if len(parts) != 5 || parts[0] != "DV1" {
		return
	}
	ip := parts[1]
	prefix, err := strconv.Atoi(parts[2])
	if err != nil || prefix < 1 || prefix > 32 || net.ParseIP(ip).To4() == nil {
		return
	}
	gateway := parts[3]
	if net.ParseIP(gateway).To4() == nil {
		return
	}
	dataDisk := parts[4] == "1"
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'
$adapter=$null
for($i=0;$i -lt 30;$i++){ $adapter=Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object {$_.Status -eq 'Up'} | Select-Object -First 1; if($adapter){break}; Start-Sleep -Seconds 1 }
if(-not $adapter){ throw '没有可用的网络适配器' }
Set-NetIPInterface -InterfaceIndex $adapter.ifIndex -Dhcp Disabled
Get-NetIPAddress -InterfaceIndex $adapter.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue
New-NetIPAddress -InterfaceIndex $adapter.ifIndex -IPAddress '%s' -PrefixLength %d -DefaultGateway '%s' | Out-Null
Set-DnsClientServerAddress -InterfaceIndex $adapter.ifIndex -ServerAddresses '%s'
if('%s' -eq '1'){
  $sys=Get-Disk | Where-Object {$_.IsSystem} | Select-Object -First 1
  $data=Get-Disk | Where-Object {$_.Number -ne $sys.Number -and $_.Size -gt 1GB} | Select-Object -First 1
  if($data){
    if($data.PartitionStyle -eq 'RAW'){ Initialize-Disk -Number $data.Number -PartitionStyle GPT -Confirm:$false }
    $part=Get-Partition -DiskNumber $data.Number -ErrorAction SilentlyContinue | Where-Object {$_.Type -eq 'Basic'} | Select-Object -First 1
    if(-not $part){ $part=New-Partition -DiskNumber $data.Number -UseMaximumSize -AssignDriveLetter }
    $vol=Get-Volume -Partition $part -ErrorAction SilentlyContinue
    if(-not $vol.DriveLetter){ Add-PartitionAccessPath -DiskNumber $data.Number -PartitionNumber $part.PartitionNumber -AssignDriveLetter }
    if($vol.FileSystem -ne 'NTFS'){ Format-Volume -Partition $part -FileSystem NTFS -NewFileSystemLabel 'DeviyData' -Confirm:$false | Out-Null }
  }
}
`, ip, prefix, gateway, gateway, map[bool]string{true: "1", false: "0"}[dataDisk])
	output, err := runPowerShell(script)
	logPath := `C:\Deviy\Logs\bootstrap.log`
	logText := fmt.Sprintf("%s ip=%s prefix=%d gateway=%s data=%v err=%v output=%s\n", time.Now().Format(time.RFC3339), ip, prefix, gateway, dataDisk, err, strings.TrimSpace(string(output)))
	_ = os.WriteFile(logPath, []byte(logText), 0o644)
}

func runPowerShell(script string) ([]byte, error) {
	return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script).CombinedOutput()
}

func escapePowerShell(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
