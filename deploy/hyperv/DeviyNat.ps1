[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [ValidateSet('Show', 'Add', 'Remove')]
    [string]$Action = 'Show',
    [string]$NatName = 'DeviyNAT',
    [ValidateSet('TCP', 'UDP')]
    [string]$Protocol = 'TCP',
    [int]$ExternalPort,
    [string]$InternalIP,
    [int]$InternalPort
)

$ErrorActionPreference = 'Stop'

function Assert-Administrator {
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this script as Administrator.'
    }
}

function Test-PrivateIPv4([string]$value) {
    $ip = $null
    if (-not [System.Net.IPAddress]::TryParse($value, [ref]$ip)) { return $false }
    if ($ip.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork) { return $false }
    $bytes = $ip.GetAddressBytes()
    if ($bytes[0] -eq 10) { return $true }
    if ($bytes[0] -eq 172 -and $bytes[1] -ge 16 -and $bytes[1] -le 31) { return $true }
    if ($bytes[0] -eq 192 -and $bytes[1] -eq 168) { return $true }
    return $false
}

Assert-Administrator
Import-Module Hyper-V -ErrorAction Stop

if (-not (Get-NetNat -Name $NatName -ErrorAction SilentlyContinue)) {
    throw "WinNAT instance '$NatName' not found. Run Install-DeviyAgent.ps1 first."
}

if ($Action -eq 'Show') {
    Get-NetNatStaticMapping -NatName $NatName |
        Select-Object Protocol, ExternalIPAddress, ExternalPort, InternalIPAddress, InternalPort |
        Sort-Object ExternalPort, Protocol |
        Format-Table -AutoSize
    return
}

if ($ExternalPort -lt 1 -or $ExternalPort -gt 65535) { throw 'ExternalPort must be 1-65535.' }
if ($InternalPort -lt 1 -or $InternalPort -gt 65535) { throw 'InternalPort must be 1-65535.' }
if (-not (Test-PrivateIPv4 $InternalIP)) { throw 'InternalIP must be a private IPv4 address.' }

$existing = Get-NetNatStaticMapping -NatName $NatName -ErrorAction SilentlyContinue |
    Where-Object { $_.Protocol -eq $Protocol -and $_.ExternalPort -eq $ExternalPort }

if ($Action -eq 'Remove') {
    if (-not $existing) { Write-Host 'No matching mapping.'; return }
    if ($PSCmdlet.ShouldProcess("$Protocol/$ExternalPort", 'Remove WinNAT mapping')) {
        $existing | Remove-NetNatStaticMapping
        Get-NetFirewallRule -DisplayName "Deviy-Manual-NAT-$Protocol-$ExternalPort" -ErrorAction SilentlyContinue | Remove-NetFirewallRule
        Write-Host "Removed $Protocol/$ExternalPort."
    }
    return
}

if ($PSCmdlet.ShouldProcess("$Protocol/$ExternalPort -> $InternalIP`:$InternalPort", 'Add WinNAT mapping')) {
    if ($existing) { $existing | Remove-NetNatStaticMapping }
    Add-NetNatStaticMapping -NatName $NatName -Protocol $Protocol -ExternalIPAddress '0.0.0.0' -ExternalPort $ExternalPort -InternalIPAddress $InternalIP -InternalPort $InternalPort | Out-Null
    Get-NetFirewallRule -DisplayName "Deviy-Manual-NAT-$Protocol-$ExternalPort" -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    New-NetFirewallRule -DisplayName "Deviy-Manual-NAT-$Protocol-$ExternalPort" -Direction Inbound -Action Allow -Protocol $Protocol -LocalPort $ExternalPort | Out-Null
    Write-Host "Added $Protocol/$ExternalPort -> $InternalIP`:$InternalPort"
}
