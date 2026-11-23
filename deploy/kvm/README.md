# Deviy KVM 被控（Ubuntu 22.04 + libvirt）

这套被控不依赖 Hyper-V，也不依赖 Proxmox VE。默认模式是：

```text
Ubuntu 22.04 LTS
  └── KVM / QEMU
        └── libvirt
              ├── virsh / virt-install
              ├── default NAT 网络（virbr0）
              └── nftables 端口转发
```

## 文件

```text
install-deviy-kvm.sh       一键安装脚本
deviy-agent-linux-amd64    Linux amd64 被控二进制
README.md                  本说明
```

## 安装模式

脚本支持两个模式：

| 模式 | 说明 |
|---|---|
| `bare` | 在 Ubuntu 22.04 裸系统上安装 KVM、libvirt 和被控 |
| `pve` | 已安装 Proxmox VE 的兼容模式；只安装 PVE provider Agent |

> 新 KVM 套件推荐使用 `bare`。`pve` 只是兼容已有 PVE 节点，不会把 Ubuntu 转成 PVE，也不会在 PVE 上安装 libvirt。

## 一键安装

把两个文件上传到 Ubuntu 22.04 主机，然后：

```bash
sudo bash install-deviy-kvm.sh \
  --mode bare \
  --token "至少32位随机Token" \
  --node-id "kvm-hk-01" \
  --node-name "KVM-HK-01" \
  --region "HongKong" \
  --controller-ip "Controller公网IP/32"
```

如果不传 `--mode`，脚本会提示选择：

```text
1) Bare Ubuntu 22.04 + KVM/libvirt
2) Existing Proxmox VE
```

如果 KVM/libvirt 已经安装，或宿主机有无法立即修复的 apt 锁，可以加：

```bash
--skip-packages
```

脚本随后只安装 Agent 和 NAT/管理防火墙配置。

脚本会安装：

```text
qemu-kvm
libvirt-daemon-system
libvirt-clients
virtinst
qemu-utils
cloud-image-utils
genisoimage
nftables
```

并执行：

- 启动 `libvirtd`
- 创建/启动 `default` NAT 网络（`virbr0`，`192.168.122.0/24`）
- 创建 KVM 存储目录
- 开启 IPv4 forward
- 安装 `deviy-agent`
- 启动 systemd 服务
- 做 `/health` 检查

### 不会做的事情

- 不修改 `eno1`、netplan、默认路由
- 不删除 Docker / Pterodactyl 网桥
- 不重装系统
- 不改 SSH 端口
- 不覆盖现有 nftables 主表；Agent 只管理 `inet deviy_nat`

## 安装后检查

```bash
systemctl status deviy-agent
virsh net-info default
virsh list --all
curl http://127.0.0.1:8443/health
```

## Controller 添加宿主机

主控「宿主机管理」→「+ 添加宿主机」：

```text
虚拟化类型：Ubuntu KVM / libvirt
节点 ID：kvm-hk-01
节点名称：KVM-HK-01
节点 IP / 域名：114.28.145.48
区域：香港
Agent 地址：http://114.28.145.48:8443
Agent Token：与安装参数一致
公网交换机：virbr0
内网交换机：virbr0
IP 池：192.168.122.0/24 私网池
端口范围：20000-30000
```

### IP 池建议

KVM/libvirt 默认 NAT 网络是：

```text
Network: default
Bridge:  virbr0
Subnet:  192.168.122.0/24
Gateway: 192.168.122.1
DHCP:    192.168.122.2 - 192.168.122.254
```

在 Controller「IP 池」创建：

```text
名称：KVM-HK-PRIVATE
类型：内网/共享
区域：香港
CIDR：192.168.122.0/24
网关：192.168.122.1
起始：192.168.122.10
结束：192.168.122.200
```

## KVM VM 创建流程

```text
Controller 分配内网 IP
        ↓
Agent virt-install
        ↓
qcow2 系统盘
        ↓
default libvirt 网络（virbr0）
        ↓
VM 通过 libvirt DHCP 获取地址；当 Controller 分配了内网 IP 时，Agent 会自动向 libvirt network 写入 DHCP reservation，保证 VM 获取到 Controller/IP 池规划的地址。
        ↓
Controller 自动申请 NAT 外部端口
        ↓
nftables DNAT：公网IP:端口 → VM内网IP:内部端口
```

当前 KVM Provider 使用 `virt-install` 创建 domain，VM ID 就是 libvirt domain 名称。

### 系统镜像

`Image` 可以填一个已有的 qcow2 路径，Agent 会用：

```bash
qemu-img create -f qcow2 -F qcow2 -b <base-image> <name>.qcow2
```

如果镜像为空，则创建空白 qcow2。

建议使用 cloud image + cloud-init 模板，在模板内预置 SSH key、用户和网络配置。

### Cloud-init

创建 KVM VM 时填写「SSH 公钥」后，Agent 会生成 cloud-init seed ISO：

```text
<name>-seed/user-data
<name>-seed/meta-data
<name>-seed/seed.iso
```

自动创建用户：

```text
deviy
sudo: ALL=(ALL) NOPASSWD:ALL
```

并注入 SSH 公钥。SSH 公钥必须是单行 `ssh-ed25519 ...` 或 `ssh-rsa ...`。

## VNC

KVM VM 的 VNC 只监听宿主机 `127.0.0.1`。Controller 虚拟机列表的 **VNC** 按钮会返回：

```text
VNC 端口
SSH 隧道命令
```

连接方式：

```bash
ssh -L 5900:127.0.0.1:5900 root@<KVM宿主机>
```

然后用 VNC Viewer 连接 `127.0.0.1:5900`。不要直接把 5900 暴露到公网。

## NAT 网络

KVM 出站：

```text
VM 192.168.122.x
   → virbr0
   → nftables masquerade
   → eno1
   → 公网
```

入站：

```text
公网 114.28.145.48:20000
   → nftables DNAT
   → 192.168.122.10:22
```

Agent 网络后端：

```text
--network-backend=nftables
--wan-interface=eno1
--nat-cidr=192.168.122.0/24
```

`--wan-interface` 由脚本自动从默认路由读取，不需要手工写死。

## 安全

- Agent 只允许 Controller IP 访问 8443。
- 如果 UFW 未启用，脚本会创建独立的 `inet deviy_mgmt` nftables 表，只放行 Controller 源地址。
- 不要将 8443 暴露给 `0.0.0.0/0`。
- 生产环境建议给 Agent 增加 TLS/mTLS 反向代理。
- `--token` 使用 32 位以上随机值。
- nftables 表名固定为 `inet deviy_nat`，不会覆盖 Docker/Pterodactyl 的表。
- 部署前确认 `192.168.122.0/24` 没有和 VPN/内网冲突。

## 故障排查

```bash
journalctl -u deviy-agent -n 100 --no-pager
virsh net-info default
virsh list --all
nft list table inet deviy_nat
ip -br a
```

如果 `virbr0` 未创建：

```bash
virsh net-start default
virsh net-autostart default
```

如果 VM 无法上网：

```bash
sysctl net.ipv4.ip_forward
nft list table inet deviy_nat
virsh net-dhcp-leases default
```

如果 Controller 无法连接 Agent：

```bash
ss -ltnp | grep 8443
curl http://127.0.0.1:8443/health
```

## PVE 兼容模式

如果选择 `pve`：

```bash
sudo bash install-deviy-kvm.sh \
  --mode pve \
  --token "Agent Token" \
  --pve-url "https://127.0.0.1:8006" \
  --pve-token "PVE_API_TOKEN" \
  --pve-node "pve" \
  --pve-storage "local-lvm" \
  --pve-bridge "vmbr0"
```

这个模式只适用于已有 Proxmox VE；它不是新的 KVM 套件，也不会安装 libvirt。
