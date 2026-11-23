# Deviy Hyper-V 被控（WinNAT 版）

这个目录是 Hyper-V 宿主机的被控程序安装包。网络转发使用 Windows 自带的 **WinNAT**，不使用 `portproxy`，也不使用 iKuai / iptables / nftables。

## 文件

```text
Install-DeviyAgent.ps1   安装 Internal 交换机、WinNAT、Agent 服务和防火墙
DeviyNat.ps1             手工查看/添加/删除 WinNAT 端口映射
deviy-agent.exe          Windows amd64 被控程序
```

## 架构

```text
Controller / 财务系统
        │  HTTPS + Bearer Token / mTLS（建议）
        ▼
Windows Hyper-V 宿主机
  DeviyAgent :8443
        │  PowerShell
        ├─ Hyper-V：创建/启动/停止/删除 VM
        ├─ WinNAT：Add-NetNatStaticMapping
        ├─ Windows Firewall：放行外部端口
        └─ Hyper-V Internal Switch：Deviy-Internal
                 │
            VM 10.0.0.10
            gateway 10.0.0.1
```

公网入站：

```text
客户端 → 公网 IP:20000
       → WinNAT StaticMapping
       → 10.0.0.10:22
```

出站：

```text
VM 10.0.0.10 → 10.0.0.1（Internal 网关）
             → WinNAT 自动 SNAT
             → 公网
```

出站由 WinNAT 自动完成，不需要 RRAS，也不需要额外的 NAT 规则。

## 安装

在 Hyper-V 宿主机上以 **管理员 PowerShell** 执行：

```powershell
Set-ExecutionPolicy Bypass -Scope Process -Force

.\Install-DeviyAgent.ps1 `
  -NodeId "hyperv-hk-01" `
  -NodeName "HYPERV-HK-01" `
  -Region "HongKong" `
  -AgentToken "请替换为随机长 Token" `
  -Listen "0.0.0.0:8443" `
  -SwitchName "Deviy-Internal" `
  -NatName "DeviyNAT" `
  -NatCIDR "10.0.0.0/24" `
  -GatewayIP "10.0.0.1" `
  -AllowedControllerAddress "10.0.0.5"
```

`AllowedControllerAddress` 必须是 Controller 或反向代理的私网地址/VPN 地址。不要把 8443 直接暴露给公网。

### 建议的端口与地址

| 项目 | 值 |
|---|---|
| 管理 API | `0.0.0.0:8443`，仅允许 Controller 私网地址 |
| Internal 网关 | `10.0.0.1/24` |
| WinNAT 名称 | `DeviyNAT` |
| WinNAT 前缀 | `10.0.0.0/24` |
| VM 网段 | `10.0.0.0/24` |
| VM 网关 | `10.0.0.1` |
| 公网端口范围 | 由 Controller 宿主机端口范围决定 |

## Controller 添加宿主机

在 Controller「宿主机管理」中填写：

```text
虚拟化类型：Hyper-V
节点 ID：hyperv-hk-01
节点名称：HYPERV-HK-01
Agent 地址：http://<宿主机私网 IP>:8443
Agent Token：与安装参数一致
区域：香港
IP 池：选择该区域的私网 IP 池
公网交换机：Deviy-Internal
内网交换机：Deviy-Internal
```

Hyper-V 的实际交换机名由 Agent 的 `--hyperv-switch` 决定；Controller 表单里的交换机字段主要用于 PVE，Hyper-V 场景建议都填 `Deviy-Internal`，保持配置可读。

如果 Controller 与 Hyper-V 不在同一内网，建议使用 WireGuard/专线，或者给 Agent 加 Nginx mTLS 反向代理，不要把裸 HTTP 暴露到公网。

## NAT 管理

### 查看当前映射

```powershell
.\DeviyNat.ps1 -Action Show
```

### 手工添加

```powershell
.\DeviyNat.ps1 -Action Add -Protocol TCP -ExternalPort 20000 -InternalIP 10.0.0.10 -InternalPort 22
```

### 手工删除

```powershell
.\DeviyNat.ps1 -Action Remove -Protocol TCP -ExternalPort 20000
```

Agent 自己同步时执行的是：

```powershell
Add-NetNatStaticMapping `
  -NatName "DeviyNAT" `
  -Protocol TCP `
  -ExternalIPAddress "0.0.0.0" `
  -ExternalPort 20000 `
  -InternalIPAddress "10.0.0.10" `
  -InternalPort 22
```

删除：

```powershell
Get-NetNatStaticMapping -NatName "DeviyNAT" |
  Where-Object { $_.ExternalPort -eq 20000 -and $_.Protocol -eq "TCP" } |
  Remove-NetNatStaticMapping
```

## 为什么不用 portproxy

`netsh interface portproxy` 是用户态转发，依赖 IP Helper，行为与 WinNAT 不同，且不是 WinNAT 静态映射。这个版本明确使用：

```text
Get-NetNat / New-NetNat
Get-NetNatStaticMapping
Add-NetNatStaticMapping
Remove-NetNatStaticMapping
```

自动安装脚本会检查这些 cmdlet；缺失时会直接报错，不会静默降级。

## 安全检查清单

- Agent 只监听私网/VPN 地址，Windows Firewall 只允许 Controller 地址访问 8443。
- `AgentToken` 至少 32 位随机值；Controller 宿主机配置必须一致。
- 生产环境给 Agent 配 `-TLSCert`、`-TLSKey`，并让 Controller 使用 HTTPS。
- 多个 Controller 时可加 `-TLSClientCA` 启用 mTLS。
- 不要使用公网 `0.0.0.0/0` 作为 `AllowedControllerAddress`。
- WinNAT 使用独立实例 `DeviyNAT`；不要和其他产品共用。
- 不要把 Internal 交换机桥接到物理网卡；VM 只挂 Internal，公网入口由 WinNAT 端口映射完成。
- 定期检查：

```powershell
Get-NetNat
Get-NetNatStaticMapping -NatName DeviyNAT
Get-Service DeviyAgent
Invoke-RestMethod http://127.0.0.1:8443/health
```

## 故障排查

### Agent 起不来

```powershell
Get-Service DeviyAgent
Get-WinEvent -LogName Application -MaxEvents 30
```

### NAT 实例不存在

```powershell
Get-NetNat -Name DeviyNAT
```

不存在时重新运行安装脚本，或手工：

```powershell
New-NetNat -Name DeviyNAT -InternalIPInterfaceAddressPrefix 10.0.0.0/24
```

### VM 能上内网但不能出公网

- 检查 VM 网关是否为 `10.0.0.1`
- 检查 VM DNS
- 检查 `Get-NetNat -Name DeviyNAT`
- 检查 Windows 主机自身是否能访问公网

### 外部端口不通

```powershell
Get-NetNatStaticMapping -NatName DeviyNAT |
  Where-Object { $_.ExternalPort -eq 20000 }
Get-NetFirewallRule -DisplayName 'Deviy-NAT-*'
Test-NetConnection 10.0.0.10 -Port 22
```

### WinNAT 与 VPN/其他 NAT 冲突

不要共用 NAT 实例。`New-NetNat` 的 `InternalIPInterfaceAddressPrefix` 必须是 Hyper-V Internal 网段，不能和 VPN 网段重叠。
