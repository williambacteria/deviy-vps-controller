[CmdletBinding()]
param(
    [switch]$NoPause,
    [switch]$NoNotify
)

$ErrorActionPreference = 'Stop'
$script:LogDir = 'C:\Deviy\Logs'
$script:LogFile = Join-Path $script:LogDir ('windows-virtio-{0}.log' -f (Get-Date -Format 'yyyyMMdd-HHmmss'))
$script:TranscriptFile = $script:LogFile + '.transcript.log'
$script:TranscriptStarted = $false

function Write-Log {
    param([string]$Message, [ConsoleColor]$Color = [ConsoleColor]::Gray)
    $line = '[{0}] {1}' -f (Get-Date -Format 'HH:mm:ss'), $Message
    Write-Host $line -ForegroundColor $Color
    Add-Content -LiteralPath $script:LogFile -Value $line -Encoding UTF8
}

function Write-Step {
    param([int]$Number, [int]$Total, [string]$Message)
    Write-Progress -Id 1 -Activity 'Deviy VirtIO Tools Installation' -Status ("Step {0}/{1}: {2}" -f $Number, $Total, $Message) -PercentComplete ([math]::Round($Number / $Total * 100))
    Write-Host ("`n[{0}/{1}] {2}" -f $Number, $Total, $Message) -ForegroundColor Cyan
}

function Show-Result {
    param([string]$Title, [string]$Message, [string]$Icon = 'Information')
    if ($NoNotify) {
        Write-Host 'Notification suppressed (-NoNotify).' -ForegroundColor DarkGray
        return
    }
    try {
        Add-Type -AssemblyName System.Windows.Forms -ErrorAction Stop
        $iconValue = [System.Windows.Forms.MessageBoxIcon]::Information
        try { $iconValue = [System.Enum]::Parse([System.Windows.Forms.MessageBoxIcon], $Icon, $true) } catch {}
        [System.Windows.Forms.MessageBox]::Show($Message, $Title, [System.Windows.Forms.MessageBoxButtons]::OK, $iconValue) | Out-Null
    }
    catch {
        Write-Host "`n$Title`n$Message" -ForegroundColor Yellow
    }
}

try {
    New-Item -ItemType Directory -Force -Path $script:LogDir | Out-Null
    try {
        Start-Transcript -Path $script:TranscriptFile -Append -ErrorAction Stop | Out-Null
        $script:TranscriptStarted = $true
    }
    catch {
        Write-Host "Transcript unavailable: $_" -ForegroundColor Yellow
    }

    Write-Host '============================================================' -ForegroundColor DarkCyan
    Write-Host ' Deviy Windows VirtIO Tools Installer' -ForegroundColor White
    Write-Host '============================================================' -ForegroundColor DarkCyan
    Write-Host 'This script only installs virtio-win-guest-tools. It does NOT change firewall, RDP, services, power or network settings.' -ForegroundColor DarkGray

    Write-Step 1 2 'Checking administrator privileges'
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Administrator privileges are required. Right-click PowerShell and choose Run as administrator.'
    }
    $os = Get-CimInstance Win32_OperatingSystem
    Write-Log ("Windows: {0} build {1}" -f $os.Caption, $os.BuildNumber) Green
    Write-Log ("Architecture: {0}" -f $os.OSArchitecture) Gray

    Write-Step 2 2 'Installing virtio-win guest tools'
    $installer = Join-Path $PSScriptRoot 'virtio-win-guest-tools.exe'
    if (-not (Test-Path -LiteralPath $installer)) {
        throw "virtio-win-guest-tools.exe not found next to this script: $PSScriptRoot"
    }
    $installerInfo = Get-Item -LiteralPath $installer
    Write-Log ("Installer: {0}" -f $installer) Gray
    Write-Log ("Installer version: {0}" -f $installerInfo.VersionInfo.FileVersion) Gray
    Write-Log 'Running silent installation (/S); this may take several minutes...' Yellow
    $process = Start-Process -FilePath $installer -ArgumentList '/S' -Wait -PassThru
    if ($process.ExitCode -ne 0) {
        throw "virtio-win guest tools installer exited with code $($process.ExitCode)"
    }
    Write-Log 'virtio-win guest tools installer completed with exit code 0.' Green

    Write-Log 'Checking installed VirtIO / QEMU components...' Cyan
    $devices = @(Get-PnpDevice -PresentOnly -ErrorAction SilentlyContinue |
        Where-Object { $_.FriendlyName -match 'VirtIO|Red Hat|QEMU' -and $_.Status -eq 'OK' })
    if ($devices.Count -gt 0) {
        foreach ($device in $devices) {
            Write-Log ("Device: {0} [{1}]" -f $device.FriendlyName, $device.Class) Green
        }
    }
    else {
        Write-Log 'No active VirtIO PnP device found. Drivers may still be installed; attach virtio hardware to test.' Yellow
    }
    $drivers = @(Get-CimInstance Win32_PnPSignedDriver -ErrorAction SilentlyContinue |
        Where-Object { $_.DeviceName -match 'VirtIO|Red Hat|QEMU' })
    Write-Log ("VirtIO signed driver entries: {0}" -f $drivers.Count) Gray
    $qemuAgent = Get-Service -Name 'qemu-ga' -ErrorAction SilentlyContinue
    if ($qemuAgent) {
        Write-Log ("QEMU Guest Agent service: {0}" -f $qemuAgent.Status) Green
    }
    else {
        Write-Log 'QEMU Guest Agent service (qemu-ga) not found.' Yellow
    }

    Write-Progress -Id 1 -Activity 'Deviy VirtIO Tools Installation' -Completed
    Write-Host "`n============================================================" -ForegroundColor Green
    Write-Host ' VIRTIO TOOLS INSTALLATION COMPLETED' -ForegroundColor Green
    Write-Host '============================================================' -ForegroundColor Green
    Write-Host ("Windows : {0} build {1}" -f $os.Caption, $os.BuildNumber) -ForegroundColor White
    Write-Host ("Installer: {0}" -f $installerInfo.VersionInfo.FileVersion) -ForegroundColor White
    Write-Host ("Log     : {0}" -f $script:LogFile) -ForegroundColor White
    Write-Host 'Reboot Windows after installation.' -ForegroundColor White
    Show-Result -Title 'Deviy VirtIO Tools' -Message ("Installation completed successfully.`n`nWindows: {0} build {1}`nInstaller: {2}`nLog: {3}`n`nPlease reboot Windows." -f $os.Caption, $os.BuildNumber, $installerInfo.VersionInfo.FileVersion, $script:LogFile) -Icon Information
}
catch {
    Write-Progress -Id 1 -Activity 'Deviy VirtIO Tools Installation' -Completed
    Write-Host "`n============================================================" -ForegroundColor Red
    Write-Host ' VIRTIO TOOLS INSTALLATION FAILED' -ForegroundColor Red
    Write-Host '============================================================' -ForegroundColor Red
    Write-Log ("ERROR: {0}" -f $_.Exception.Message) Red
    Show-Result -Title 'Deviy VirtIO Tools - Failed' -Message ("Installation failed:`n`n{0}`n`nLog: {1}" -f $_.Exception.Message, $script:LogFile) -Icon Error
    exit 1
}
finally {
    if ($script:TranscriptStarted) {
        try { Stop-Transcript | Out-Null } catch {}
    }
    if (-not $NoPause) {
        Write-Host "`nPress Enter to close this window..." -ForegroundColor DarkGray
        try { Read-Host | Out-Null } catch {}
    }
}
