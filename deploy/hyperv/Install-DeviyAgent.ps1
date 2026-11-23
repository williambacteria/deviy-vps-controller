[CmdletBinding()]
param(
    [string]$InstallDir = 'C:\Deviy\Agent',
    [string]$NodeId = 'hyperv-01',
    [string]$NodeName = 'HYPERV-01',
    [string]$Region = 'HongKong',
    [string]$AgentToken,
    [string]$Listen = '0.0.0.0:8443',
    [string]$SwitchName = 'Deviy-Internal',
    [string]$NatName = 'DeviyNAT',
    [string]$NatCIDR = '10.0.0.0/24',
    [string]$GatewayIP = '10.0.0.1',
    [string]$VHDDir = 'C:\Deviy\VHDs',
    [string]$ControllerURL = '',
    [string]$RegisterToken = '',
    [string]$TLSCert = '',
    [string]$TLSKey = '',
    [string]$TLSClientCA = '',
    [string[]]$AllowedControllerAddress = @()
)

$ErrorActionPreference = 'Stop'

function Assert-Administrator {
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this script as Administrator.'
    }
}

function Assert-HyperVNat {
    Import-Module Hyper-V -ErrorAction Stop
    foreach ($cmd in @('Get-VMSwitch', 'New-VMSwitch', 'Get-NetNat', 'New-NetNat', 'Get-NetNatStaticMapping', 'Add-NetNatStaticMapping', 'Remove-NetNatStaticMapping')) {
        if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
            throw "Required Hyper-V/WinNAT cmdlet is missing: $cmd"
        }
    }
}

Assert-Administrator
Assert-HyperVNat

if ([string]::IsNullOrWhiteSpace($AgentToken)) {
    throw 'AgentToken is required. Use the same token when adding the host in the Controller.'
}
if (-not (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'deviy-agent.exe'))) {
    throw "deviy-agent.exe not found next to this script: $PSScriptRoot"
}

# 1. Internal switch for VMs. VMs get a NIC on this switch, not on the physical WAN NIC.
$switch = Get-VMSwitch -Name $SwitchName -ErrorAction SilentlyContinue
if (-not $switch) {
    New-VMSwitch -Name $SwitchName -SwitchType Internal | Out-Null
    Write-Host "Created internal switch: $SwitchName"
} else {
    if ($switch.SwitchType -ne 'Internal') {
        throw "Switch '$SwitchName' exists but is not an Internal switch."
    }
    Write-Host "Using existing internal switch: $SwitchName"
}

# 2. Gateway IP on the host vEthernet adapter.
$adapterName = "vEthernet ($SwitchName)"
$adapter = Get-NetAdapter -Name $adapterName -ErrorAction SilentlyContinue
if (-not $adapter) {
    throw "Adapter not found: $adapterName"
}
$gateway = Get-NetIPAddress -InterfaceIndex $adapter.ifIndex -IPAddress $GatewayIP -AddressFamily IPv4 -ErrorAction SilentlyContinue
if (-not $gateway) {
    New-NetIPAddress -InterfaceIndex $adapter.ifIndex -IPAddress $GatewayIP -PrefixLength 24 -ErrorAction Stop | Out-Null
    Write-Host "Assigned NAT gateway: $GatewayIP/24"
}

# 3. Dedicated NAT instance. Do not share another product's NAT instance.
$nat = Get-NetNat -Name $NatName -ErrorAction SilentlyContinue
if (-not $nat) {
    New-NetNat -Name $NatName -InternalIPInterfaceAddressPrefix $NatCIDR | Out-Null
    Write-Host "Created WinNAT instance: $NatName ($NatCIDR)"
} else {
    $existingPrefix = [string]$nat.InternalIPInterfaceAddressPrefix
    if ($existingPrefix -ne $NatCIDR) {
        throw "NAT '$NatName' already exists with prefix '$existingPrefix', expected '$NatCIDR'. Refusing to modify it."
    }
    Write-Host "Using existing WinNAT instance: $NatName ($NatCIDR)"
}

# 4. Install directory and binary.
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $VHDDir | Out-Null
$targetExe = Join-Path $InstallDir 'deviy-agent.exe'
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'deviy-agent.exe') -Destination $targetExe -Force

# 5. Recreate the service with the current arguments.
$serviceName = 'DeviyAgent'
$existing = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
if ($existing) {
    if ($existing.Status -ne 'Stopped') { Stop-Service -Name $serviceName -Force }
    & sc.exe delete $serviceName | Out-Null
    Start-Sleep -Seconds 2
}

$agentArgs = @(
    "--listen=$Listen",
    '--provider=hyperv',
    '--mode=hyperv',
    "--node-id=$NodeId",
    "--name=$NodeName",
    "--region=$Region",
    "--token=$AgentToken",
    '--network-backend=windows',
    "--nat-cidr=$NatCIDR",
    '--allow-network-apply=true',
    '--metrics-backend=windows',
    "--hyperv-switch=$SwitchName",
    "--hyperv-storage=$VHDDir"
)
if ($ControllerURL) {
    $agentArgs += "--controller-url=$ControllerURL"
    if ($RegisterToken) { $agentArgs += "--register-token=$RegisterToken" }
}
if ($TLSCert) { $agentArgs += "--tls-cert=$TLSCert" }
if ($TLSKey) { $agentArgs += "--tls-key=$TLSKey" }
if ($TLSClientCA) { $agentArgs += "--tls-client-ca=$TLSClientCA" }

$binaryPath = '"' + $targetExe + '" ' + ($agentArgs -join ' ')
New-Service -Name $serviceName -BinaryPathName $binaryPath -DisplayName 'Deviy Hyper-V Agent' -StartupType Automatic | Out-Null
& sc.exe description $serviceName 'Deviy Cloud Hyper-V agent (WinNAT + Hyper-V)' | Out-Null
Start-Service -Name $serviceName

# 6. Restrict the management API with Windows Firewall. Do not expose it to the Internet.
if ($AllowedControllerAddress.Count -gt 0) {
    Get-NetFirewallRule -DisplayName 'Deviy-Agent-API' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    New-NetFirewallRule -DisplayName 'Deviy-Agent-API' -Direction Inbound -Action Allow -Protocol TCP -LocalPort ([int]($Listen.Split(':')[-1])) -RemoteAddress $AllowedControllerAddress | Out-Null
    Write-Host "Allowed controller addresses: $($AllowedControllerAddress -join ', ')"
} else {
    Write-Warning 'No AllowedControllerAddress supplied. The agent listens on the network but no inbound firewall rule was created. Add a private/VPN source address or use a reverse proxy with mTLS.'
}

# 7. Health check.
Start-Sleep -Seconds 2
$healthURL = "http://127.0.0.1:$([int]($Listen.Split(':')[-1]))/health"
try {
    $health = Invoke-RestMethod -Uri $healthURL -TimeoutSec 10
    Write-Host "Agent health: $($health | ConvertTo-Json -Compress)"
} catch {
    throw "Agent did not answer on $healthURL. Check: Get-Service DeviyAgent; Get-WinEvent -LogName Application -MaxEvents 20"
}

Write-Host ''
Write-Host 'Deviy Hyper-V agent installed.'
Write-Host "  Service: $serviceName"
Write-Host "  API: $Listen"
Write-Host "  Internal switch: $SwitchName"
Write-Host "  WinNAT: $NatName ($NatCIDR)"
Write-Host "  Controller host URL: http://<this-host>:$([int]($Listen.Split(':')[-1]))"
