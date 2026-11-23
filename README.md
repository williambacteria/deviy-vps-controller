# Deviy 虚拟化主控、宿主机 Agent 与虚拟机 Guest Agent

对应 demovps.deviy.cn 的 Go 工程与配套部署资料，包含集中控制台、宿主机被控和虚拟机内部辅助程序。

**2026-11-24 已找回并补充 Go 源码。** 源码来自项目所有者指定的本地 Deviy.cn 工程，含两个 Go Module。本仓库不再仅是可执行程序归档。按照所有者本次确认，仓库保持公开；生产密钥、证书、节点状态、客户数据、镜像和磁盘不在仓库内。

## 一、三个程序分别装在哪里

| 程序 | 源码入口 | 安装位置 | 作用 |
| --- | --- | --- | --- |
| Deviy Controller | controller/cmd/deviy-controller | 集中主控服务器 | 节点聚合、Web 管理、镜像、IP 池、主机配置、操作代理 |
| Deviy Agent | agent/cmd/deviy-agent | 实际虚拟化宿主机 | 对接 KVM/libvirt、PVE 或 Hyper-V，执行 VM、NAT、监控和 Guest 操作 |
| Deviy Guest Agent | agent/cmd/deviy-guest-agent | Windows / Linux 客户虚拟机内部 | 修改密码、设置网络/DNS/主机名、磁盘扩容等 |
| deviy-cert | agent/cmd/deviy-cert | 管理员离线配置环境 | 生成测试/部署用 mTLS 证书，生成结果不提交仓库 |

“给客户虚拟机安装的控制程序”对应第三行，不能用宿主机 Agent 替代。

~~~text
财务系统 / 管理员
        ↓
集中 Controller（demovps.deviy.cn）
        ↓ Bearer Token / 按配置使用 mTLS
宿主机 Agent
        ├── KVM/libvirt、PVE API 或 Hyper-V
        ├── VM 状态、IP 发现、监控、NAT 映射
        └── 宿主机私网 → 虚拟机内部 Guest Agent
                              └── 密码、网络、DNS、磁盘等系统操作
~~~

配套财务项目为 [deviy-finance](https://github.com/WilliamBacteria/deviy-finance)。财务、宿主机和 Guest 的接口凭据属于不同层级，不能互相混用。

## 二、用户关心的功能在哪里

### 修改虚拟机登录密码

- Guest HTTP 入口：agent/cmd/deviy-guest-agent/main.go 中的 POST /password。
- Windows 实现：guest_windows.go；Linux 实现：guest_linux.go。
- Linux 调用 chpasswd 等系统工具；Windows 使用相应本机用户管理命令。
- 宿主机转发：agent/internal/server/server.go 的 handleVMGuestPassword，经 /api/vms/{id}/guest/password 转向 VM 私网的 8099 端口。
- Controller 负责将受认证操作转发到对应节点。

Guest Agent 默认允许来源 192.168.122.1/32；另可配置 X-Deviy-Token。实际网桥地址不同就必须调整 --allow。当前代码允许 Token 为空，此时依赖来源 CIDR 限制；上线应配置专用随机 Token、核对网桥与防火墙，不能把 8099 暴露为公网通用管理接口。

### 获取虚拟机 IP

IP 发现主要位于宿主机 provider 层，例如 agent/internal/provider/kvm.go 中 dhcpIP、configuredIP。PVE、Hyper-V 各有自身适配。Guest 的 POST /network 是修改网络参数，**不是用于查询 IP 的 GET 接口**；不要将配置接口当成查询接口。

### Guest Agent 其他入口

| 路径 | 方法 | 作用 |
| --- | --- | --- |
| /health | 当前 handler 返回健康信息 | 确认程序可达 |
| /password | POST | 修改指定用户密码 |
| /network | POST | 设置 IPv4、掩码前缀、网关和 DNS |
| /dns | POST | 设置 DNS |
| /datadisk | POST | 数据盘配置 |
| /resize | POST | 扩容 |
| /reboot | POST | 重启 |
| /hostname | POST | 设置主机名 |

入口统一经过来源 CIDR 检查与已配置的 Token 检查。Windows 和 Linux 实现有差异，具体以各平台源码为准。Windows 还包含从 SMBIOS 读取初始化信息的逻辑；不能把其中的网络初始化行为误认为一般只读监控。

## 三、目录结构

~~~text
controller/
  go.mod                         deviy.cloud/controller
  cmd/deviy-controller/          主控入口
  cmd/mtls-check/                mTLS 检查辅助程序
  internal/controller/          HTTP、认证、节点、IP 池和主机管理
  internal/controller/web/      Go embed 的 HTML、Logo、noVNC 等资源
agent/
  go.mod                         deviy.cloud/agent
  cmd/deviy-agent/               宿主机被控
  cmd/deviy-guest-agent/         Windows/Linux 客户机内程序
  cmd/deviy-cert/                证书生成器
  internal/provider/            KVM、PVE、Hyper-V、mock
  internal/server/              Agent HTTP API
  internal/network/             nftables / Windows 网络适配
  internal/metrics/             Linux / Windows 指标采集
  internal/heartbeat/           节点注册与心跳
  internal/tlsutil/             TLS、mTLS、证书辅助
ui-azure/                        现网通过 Nginx 注入的独立 UI 层
deploy/
  kvm/                          KVM/PVE 安装脚本及说明
  hyperv/                       Hyper-V / WinNAT 安装脚本及说明
  windows/                      Guest Agent、VirtIO 安装/校验脚本
  deviy-controller.service       主控 systemd 示例
  controller.env.example        无密钥环境变量模板
  controller-state.example.json 空状态模板
  nginx.example.conf            HTTPS 与 UI 注入示例
bin/deviy-controller            原现网 Linux x86-64 程序归档
CHECKSUMS.sha256                 原归档程序哈希
scripts/verify-artifact.py       校验原归档程序
~~~

两个 Module 当前只引用 Go 标准库，没有外部 require，因此没有人为补造 go.sum。noVNC 的第三方静态资源与许可随源目录保留。

## 四、构建环境与步骤

两个 go.mod 均声明 **Go 1.27**；本次核对使用 **Go 1.27.3 / Windows amd64**。使用匹配工具链，不要为绕过版本检查随意降低 go.mod。

### 在 Linux/macOS shell 构建

在仓库根目录执行：

~~~bash
mkdir -p dist
cd controller
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../dist/deviy-controller-linux-amd64 ./cmd/deviy-controller
cd ../agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../dist/deviy-agent-linux-amd64 ./cmd/deviy-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o ../dist/deviy-agent-windows-amd64.exe ./cmd/deviy-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o ../dist/deviy-guest-agent-windows-amd64.exe ./cmd/deviy-guest-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../dist/deviy-guest-agent-linux-amd64 ./cmd/deviy-guest-agent
cd ..
~~~

### 在 Windows PowerShell 构建

~~~powershell
New-Item -ItemType Directory -Force dist | Out-Null
$env:CGO_ENABLED = '0'
$env:GOARCH = 'amd64'
$env:GOOS = 'linux'
Set-Location controller
go build -o ../dist/deviy-controller-linux-amd64 ./cmd/deviy-controller
Set-Location ../agent
go build -o ../dist/deviy-agent-linux-amd64 ./cmd/deviy-agent
go build -o ../dist/deviy-guest-agent-linux-amd64 ./cmd/deviy-guest-agent
$env:GOOS = 'windows'
go build -o ../dist/deviy-agent-windows-amd64.exe ./cmd/deviy-agent
go build -o ../dist/deviy-guest-agent-windows-amd64.exe ./cmd/deviy-guest-agent
Set-Location ..
Remove-Item Env:GOOS
~~~

要使用部署安装器，将构建输出复制到安装器旁并按其要求命名：

- KVM：deviy-agent-linux-amd64。
- Hyper-V：deviy-agent.exe。
- Windows 客户机：deviy-guest-agent.exe。

仓库未包含 Windows VirtIO 第三方安装 EXE、镜像和 ISO。需要驱动时从可信官方来源单独准备。

## 五、测试与验证范围

在每个 Module 目录独立运行：

~~~bash
cd controller
go test ./...
cd ../agent
go test ./...
~~~

2026-11-24 核对记录：

- Controller 在 Windows amd64 上通过 go test ./...。
- Agent 在 Windows amd64 上通过 go test ./...，含宿主机 provider、网络、指标、HTTP、心跳及 TLS 测试。
- Linux amd64 交叉构建与 Agent Windows amd64 构建按本次记录核对。
- Guest Agent 入口没有独立单元测试，本次只验证平台构建。
- 本次未在真实宿主机执行改密码、改网、重启、磁盘格式化或虚拟机删除。

测试中的 mock 和 HTTP 假服务不能替代实际节点验收。新版本应先在隔离节点与测试 VM 验证。

## 六、版本与现网程序的对应关系

原归档 bin/deviy-controller 的 Go BuildInfo 显示：

- Module：deviy.cloud/controller。
- 入口：deviy.cloud/controller/cmd/deviy-controller。
- Go：1.27.3。
- 目标：linux/amd64，CGO_ENABLED=0。

与找回源码的模块、入口和工具链对应。本次没有把重新编译的文件替换到生产，也没有声称源码与现网二进制逐字节等同；具体行为仍需版本核对与验收。

保留 CHECKSUMS.sha256 只用于校验原归档程序。dist 新构建产物应另行记录哈希，不能用原哈希要求新文件必然一致。Go 内嵌页面和现网 ui-azure 独立展示层是两层，后续发布需要同步考虑。

## 七、集中主控部署

新安装示例：

~~~bash
python3 scripts/verify-artifact.py
install -d -m 755 /www/wwwroot/demovps.deviy.cn
install -m 755 dist/deviy-controller-linux-amd64 /www/wwwroot/demovps.deviy.cn/deviy-controller
cp -R ui-azure /www/wwwroot/demovps.deviy.cn/
~~~

全新环境可以复制 deploy/controller-state.example.json 初始化状态。**现网恢复不可用空模板覆盖原状态。**

机密配置：

~~~bash
install -d -m 700 /etc/deviy-controller
install -m 600 deploy/controller.env.example /etc/deviy-controller/controller.env
~~~

在服务器上填写 DEVIY_ADMIN_USER、DEVIY_ADMIN_PASSWORD 和 DEVIY_API_TOKEN，真实值不进入 Git。按目录和运行账户调整 deploy/deviy-controller.service，安装后运行：

~~~bash
systemctl daemon-reload
systemctl enable --now deviy-controller
systemctl status deviy-controller
~~~

参数和环境变量见 controller/cmd/deviy-controller/main.go，包括监听地址、节点注册、mTLS 与状态路径。默认使用回环监听，通过 HTTPS Nginx 对外服务。

Nginx 示例在 deploy/nginx.example.conf。/__azure/ 提供独立静态文件，sub_filter 注入主题与 UI，保留 WebSocket 升级。先 nginx -t，再 reload；宝塔中合并到现有站点，不覆盖证书和面板 PHP 配置。

## 八、宿主机与客户机部署

### KVM / PVE

阅读 deploy/kvm/README.md 和 install-deviy-kvm.sh --help。KVM 模式依赖 libvirt、virsh、virt-install 等；PVE 模式直接对接 PVE API。安装器可能安装软件包、配置 NAT 和防火墙，先核对实际网桥与管理来源。

### Hyper-V

阅读 deploy/hyperv/README.md。Agent 运行于 Hyper-V 宿主机，以 PowerShell 对接 Hyper-V、WinNAT 和 Windows Firewall。按实际 Internal Switch、CIDR、网关和允许的主控来源配置。

### Windows Guest Agent

把构建后的 deviy-guest-agent.exe 与 deploy/windows/Install-DeviyGuestAgent.ps1 放在一起，管理员 PowerShell 执行安装脚本。安装器使用计划任务启动程序，并限制 Guest 管理端口的来源。

默认 AllowedCIDR 是 192.168.122.1/32，Token 参数应使用独立随机值。安装后先用 /health 验证连通，再在测试 VM 验证操作。不要在生产 VM 通过随意修改网络或初始化数据盘来“测试安装”。

### Linux Guest Agent

构建目标为 linux/amd64，核心操作需要 root 权限。依照实际发行版准备系统工具，配置仅允许宿主机访问的管理网络和来源限制。仓库未提供经验证的通用 Linux Guest systemd 安装器，不把编译成功描述为安装已完成。

## 九、备份、恢复与上线边界

Git 不包含：

- .certs、私钥、生产 .env、节点 Token 或 Guest Token。
- controller-state.json、节点地址清单、IP 分配与客户 VM 绑定。
- .tools、.work、_radar_update、缓存、历史临时部署程序。
- 原工程中的 PHP 财务 app、数据库、日志、会话。
- ISO、QCOW2、VHDX、备份磁盘和 Windows 第三方安装包。

恢复时需要另行取得生产状态、节点凭据、证书、镜像和实际磁盘。主控、宿主机和 Guest 的版本均需确认；不能只恢复 Git 就宣称恢复了客户 VM。

升级前备份状态并记录版本，使用独立环境核对接口和权限。避免两份主控同时写同一状态，避免财务与主控重复执行开通或扣费。新源码本次仅上传仓库，没有重启或替换现网程序。

## 十、公开仓库的内容边界与许可

本次按所有者授权保持公开，并对新增文件执行敏感信息扫描。示例地址、测试证书生成代码和单元测试中的假 Token 不代表生产凭据。新增部署文件时仍应检查待提交内容，不能把客户端或机房的配置直接复制进来。

仓库公开不自动授予额外的软件许可。本次没有为用户项目擅自添加开源许可证；第三方 noVNC、pako、字体等遵循各自目录中的原许可。

## 十一、维护建议

源代码、编译产物和生产数据分别管理。按功能分支审查修改，记录构建工具链、测试结果与产物 SHA-256。Guest 密码和网络相关变更应与宿主机转发实现一起验证。现网修复应回传源码，避免只有一个新二进制而无法追踪实现。
