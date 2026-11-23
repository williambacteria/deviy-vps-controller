[CmdletBinding()]
param(
    [string]$InstallDir = 'C:\Deviy\GuestAgent',
    [string]$Token = '',
    [string]$AllowedCIDR = '192.168.122.1/32',
    [int]$Port = 8099,
    [switch]$NoPause,
    [switch]$NoNotify
)

$ErrorActionPreference = 'Stop'
$script:LogDir = 'C:\Deviy\Logs'
$script:LogFile = Join-Path $script:LogDir ('guest-agent-install-{0}.log' -f (Get-Date -Format 'yyyyMMdd-HHmmss'))
$script:TranscriptFile = $script:LogFile + '.transcript.log'
$script:TranscriptStarted = $false

function Write-Step {
    param([int]$Number, [int]$Total, [string]$Message)
    Write-Progress -Id 1 -Activity 'Deviy Guest Agent Installation' -Status ("Step {0}/{1}: {2}" -f $Number, $Total, $Message) -PercentComplete ([math]::Round($Number / $Total * 100))
    Write-Host ("[{0}/{1}] {2}" -f $Number, $Total, $Message) -ForegroundColor Cyan
}

function Write-Log {
    param([string]$Message, [ConsoleColor]$Color = [ConsoleColor]::Gray)
    $line = '[{0}] {1}' -f (Get-Date -Format 'HH:mm:ss'), $Message
    Write-Host $line -ForegroundColor $Color
    Add-Content -LiteralPath $script:LogFile -Value $line -Encoding UTF8
}

function Show-Result {
    param([string]$Title, [string]$Message, [string]$Icon = 'Information')
    if ($NoNotify) {
        Write-Host "Notification suppressed (-NoNotify)." -ForegroundColor DarkGray
        return
    }
    try {
        Add-Type -AssemblyName System.Windows.Forms -ErrorAction Stop
        $iconValue = [System.Windows.Forms.MessageBoxIcon]::Information; try { $iconValue = [System.Enum]::Parse([System.Windows.Forms.MessageBoxIcon], $Icon, $true) } catch {}; [System.Windows.Forms.MessageBox]::Show($Message, $Title, [System.Windows.Forms.MessageBoxButtons]::OK, $iconValue) | Out-Null
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
    Write-Host ' Deviy Windows Guest Agent Installer' -ForegroundColor White
    Write-Host '============================================================' -ForegroundColor DarkCyan

    Write-Step 1 6 'Checking administrator privileges'
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Administrator privileges are required. Right-click the .cmd file and choose Run as administrator.'
    }
    Write-Log 'Administrator privileges confirmed.' Green

    Write-Step 2 6 'Validating installation files and parameters'
    if (-not [string]::IsNullOrWhiteSpace($Token) -and $Token.Length -lt 24) {
        throw 'When supplied, Token must be at least 24 characters.'
    }
    $source = Join-Path $PSScriptRoot 'deviy-guest-agent.exe'
    if (-not (Test-Path -LiteralPath $source)) {
        throw "deviy-guest-agent.exe not found next to this script: $PSScriptRoot"
    }
    Write-Log "Source: $source" Gray
    Write-Log "Allowed host CIDR: $AllowedCIDR" Gray
    Write-Log ("Authentication: {0}" -f $(if ([string]::IsNullOrWhiteSpace($Token)) { 'host bridge only (no token)' } else { 'host bridge + token' })) Gray

    Write-Step 3 6 'Copying Guest Agent files'
    Get-ScheduledTask -TaskName 'DeviyGuestAgent' -ErrorAction SilentlyContinue | Stop-ScheduledTask -ErrorAction SilentlyContinue
    Get-Process -Name 'deviy-guest-agent' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $target = Join-Path $InstallDir 'deviy-guest-agent.exe'
    Copy-Item -LiteralPath $source -Destination $target -Force
    Write-Log "Copied to $target" Green

    Write-Step 4 6 'Configuring Windows Firewall'
    Get-NetFirewallRule -DisplayName 'Deviy-Guest-Agent' -ErrorAction SilentlyContinue | Remove-NetFirewallRule -ErrorAction SilentlyContinue
    New-NetFirewallRule -DisplayName 'Deviy-Guest-Agent' -Direction Inbound -Action Allow -Protocol TCP -LocalPort $Port -RemoteAddress $AllowedCIDR -Profile Any | Out-Null
    Write-Log "Firewall rule created: TCP $Port from $AllowedCIDR" Green

    Write-Step 5 6 'Registering startup scheduled task'
    $arguments = "--listen=0.0.0.0:$Port --allow=$AllowedCIDR"
    if (-not [string]::IsNullOrWhiteSpace($Token)) {
        $arguments += " --token=$Token"
    }
    $action = New-ScheduledTaskAction -Execute $target -Argument $arguments
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
    $taskPrincipal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    Unregister-ScheduledTask -TaskName 'DeviyGuestAgent' -Confirm:$false -ErrorAction SilentlyContinue
    Register-ScheduledTask -TaskName 'DeviyGuestAgent' -Action $action -Trigger $trigger -Settings $settings -Principal $taskPrincipal -Description 'Deviy guest password/template control agent' | Out-Null
    Write-Log 'Scheduled task DeviyGuestAgent registered.' Green

    Write-Step 6 6 'Starting Guest Agent and checking health'
    Start-ScheduledTask -TaskName 'DeviyGuestAgent'
    $headers = @{}
    if (-not [string]::IsNullOrWhiteSpace($Token)) { $headers['X-Deviy-Token'] = $Token }
    $health = $null
    for ($attempt = 1; $attempt -le 15; $attempt++) {
        Start-Sleep -Seconds 1
        try {
            $health = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -Headers $headers -TimeoutSec 5
            break
        }
        catch {
            Write-Progress -Id 1 -Activity 'Deviy Guest Agent Installation' -Status ("Waiting for agent health check ({0}/15)..." -f $attempt) -PercentComplete 92
        }
    }
    if ($null -eq $health) {
        $task = Get-ScheduledTask -TaskName 'DeviyGuestAgent' -ErrorAction SilentlyContinue
        throw ("Guest Agent did not answer on port {0}. Task state: {1}. Check {2}" -f $Port, $task.State, $script:LogFile)
    }
    Write-Log ("Health check OK: {0}" -f ($health | ConvertTo-Json -Compress)) Green

    Write-Progress -Id 1 -Activity 'Deviy Guest Agent Installation' -Completed
    Write-Host "============================================================" -ForegroundColor Green
    Write-Host ' INSTALLATION SUCCESSFUL' -ForegroundColor Green
    Write-Host '============================================================' -ForegroundColor Green
    Write-Host ("Service : DeviyGuestAgent") -ForegroundColor White
    Write-Host ("Target  : {0}" -f $target) -ForegroundColor White
    Write-Host ("Port    : {0}" -f $Port) -ForegroundColor White
    Write-Host ("Allow   : {0}" -f $AllowedCIDR) -ForegroundColor White
    Write-Host ("Log     : {0}" -f $script:LogFile) -ForegroundColor White
    Show-Result -Title 'Deviy Guest Agent' -Message ("Installation successful.Task: DeviyGuestAgentPort: {0}Allowed: {1}Log: {2}" -f $Port, $AllowedCIDR, $script:LogFile) -Icon Information
}
catch {
    Write-Progress -Id 1 -Activity 'Deviy Guest Agent Installation' -Completed
    Write-Host "============================================================" -ForegroundColor Red
    Write-Host ' INSTALLATION FAILED' -ForegroundColor Red
    Write-Host '============================================================' -ForegroundColor Red
    Write-Log ("ERROR: {0}" -f $_.Exception.Message) Red
    Write-Log ("Location: {0}" -f $_.InvocationInfo.PositionMessage) DarkYellow
    Show-Result -Title 'Deviy Guest Agent - Failed' -Message ("Installation failed:{0}Log: {1}" -f $_.Exception.Message, $script:LogFile) -Icon Error
    exit 1
}
finally {
    if ($script:TranscriptStarted) {
        try { Stop-Transcript | Out-Null } catch {}
    }
    if (-not $NoPause) {
        Write-Host "Press Enter to close this window..." -ForegroundColor DarkGray
        try { Read-Host | Out-Null } catch {}
    }
}
