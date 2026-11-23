[CmdletBinding()]
param([switch]$NoPause)

$ErrorActionPreference = 'Continue'
$ok = 0
$warn = 0

function Section([string]$Title) {
    Write-Host "`n=== $Title ===" -ForegroundColor Cyan
}
function Ok([string]$Message) {
    $script:ok++
    Write-Host "[OK]   $Message" -ForegroundColor Green
}
function Warn([string]$Message) {
    $script:warn++
    Write-Host "[WARN] $Message" -ForegroundColor Yellow
}
function Info([string]$Message) {
    Write-Host "[INFO] $Message" -ForegroundColor Gray
}

Write-Host 'Deviy VirtIO / Guest Tools Verification' -ForegroundColor White
Write-Host 'This checks driver installation, not whether the VM currently uses VirtIO hardware.' -ForegroundColor DarkGray

Section 'QEMU Guest Agent service'
$qga = Get-Service -Name 'qemu-ga' -ErrorAction SilentlyContinue
if ($qga) {
    if ($qga.Status -eq 'Running') { Ok "qemu-ga is Running" } else { Warn "qemu-ga exists but status is $($qga.Status)" }
    $qga | Format-List Name, DisplayName, Status, StartType
} else {
    Warn 'qemu-ga service not found.'
}

Section 'VirtIO / Red Hat / Balloon devices'
$virtioDevices = @(Get-PnpDevice -PresentOnly -ErrorAction SilentlyContinue |
    Where-Object { $_.FriendlyName -match 'VirtIO|Red Hat|Balloon' })
if ($virtioDevices.Count -gt 0) {
    Ok ("Found {0} VirtIO-related device(s)" -f $virtioDevices.Count)
    $virtioDevices | Format-Table Status, Class, FriendlyName, InstanceId -AutoSize
} else {
    Warn 'No VirtIO/Red Hat/Balloon device is present.'
}

Section 'QEMU emulated devices (NOT VirtIO)'
$qemuDevices = @(Get-PnpDevice -PresentOnly -ErrorAction SilentlyContinue |
    Where-Object { $_.FriendlyName -match 'QEMU' })
if ($qemuDevices.Count -gt 0) {
    Info ("Found {0} QEMU-emulated device(s). QEMU HARDDISK / QEMU DVD-ROM are not VirtIO devices." -f $qemuDevices.Count)
    $qemuDevices | Format-Table Status, Class, FriendlyName, InstanceId -AutoSize
}

Section 'Driver packages in Windows driver store'
$pnp = & pnputil.exe /enum-drivers 2>&1 | Out-String
$matches = [regex]::Matches($pnp, '(?im)(Published Name|Provider Name|Class Name|Driver Version)\s*:\s*(.+)')
$redhatLines = @($matches | Where-Object { $_.Groups[2].Value -match 'Red Hat|VirtIO|virtio|netkvm|viostor|vioscsi|balloon' })
if ($redhatLines.Count -gt 0) {
    Ok ("Driver-store entries matching VirtIO/Red Hat: {0}" -f $redhatLines.Count)
    $redhatLines | ForEach-Object { Write-Host ("  {0}: {1}" -f $_.Groups[1].Value, $_.Groups[2].Value) }
} else {
    Warn 'No matching VirtIO driver-store entry found (or pnputil requires Administrator).'
}

Section 'Installed files'
foreach ($path in @('C:\Program Files\Qemu-ga\qemu-ga.exe', 'C:\Program Files\Virtio-Win')) {
    if (Test-Path -LiteralPath $path) { Ok "Found $path" } else { Info "Not found: $path" }
}

Section 'Summary'
Write-Host ("Checks passed: {0}, warnings: {1}" -f $script:ok, $script:warn) -ForegroundColor White
if ($qga -or $virtioDevices.Count -gt 0) {
    Write-Host 'Guest tools appear to be installed.' -ForegroundColor Green
} else {
    Write-Host 'Guest tools may not be installed. Check the installation log.' -ForegroundColor Yellow
}
Write-Host ''
Write-Host 'Note: a VM using SATA/e1000e or QEMU emulated SCSI will show QEMU HARDDISK/QEMU DVD-ROM' -ForegroundColor DarkGray
Write-Host 'and will NOT show VirtIO disk/NIC devices even when virtio-win-guest-tools is installed.' -ForegroundColor DarkGray
Write-Host 'To test VirtIO disk/NIC, create a VM with virtio disk/network hardware.' -ForegroundColor DarkGray

if (-not $NoPause) {
    Write-Host "`nPress Enter to close..." -ForegroundColor DarkGray
    try { Read-Host | Out-Null } catch {}
}
