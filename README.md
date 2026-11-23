# Deviy VPS Controller：主控程序与管理 UI

对应站点 **demovps.deviy.cn**，为 Deviy 财务提供基础设施控制能力。本仓库为 **2026-11-23** 在用部署的私有归档，包含 Linux x86-64 可执行程序、独立 UI 静态资源、无密钥配置样例与部署说明。

## 一、重要范围说明

**没有找到 Go 后端源码。** 原站点目录包含已编译的 Go 程序 deviy-controller，没有 go.mod、main.go 或后端源代码。本次也检查了服务器常用部署路径，未发现对应 Go 工程。

因此：

- 可以在兼容的 Linux x86-64 环境运行归档的可执行程序。
- 可以修改 ui-azure 里的 JavaScript、CSS、Logo 和字体。
- 无法仅凭本仓库重新编译或修改 Go 后端。
- 其他虚拟化节点上的 Agent、镜像、磁盘与虚拟机不属于本仓库。
- 真实节点、Token、IP 池和虚拟机绑定保存在生产状态文件中，**未上传 GitHub**。

本仓库是**部署归档和 UI 代码仓库**，不是完整后端源码仓库。后续取得原 Go 工程后，应补齐版本、依赖和构建流程，再与这里的部署版本核对。

## 二、与财务站的关系

配套仓库：[deviy-finance](https://github.com/WilliamBacteria/deviy-finance)。

~~~text
客户 → www.deviy.cn 财务站
            ↓ 认证的主控 API
       Deviy Controller
            ↓ 节点协议 / Agent
       虚拟化节点与实际资源

管理员 → demovps.deviy.cn → Nginx → Controller 内嵌页面
                                     ↑
                           /__azure/ 注入的 UI
~~~

Controller 聚合节点、虚拟机、镜像和 IP 池；具体操作以当前二进制及节点能力为准。本仓库没有增加或模拟任何主控接口。

## 三、文件结构

~~~text
bin/deviy-controller             原站点 Linux x86-64 程序
ui-azure/                        独立 UI 样式、脚本、Logo、字体
deploy/controller.env.example    无密码、无 Token 的环境变量模板
deploy/controller-state.example.json  空状态模板
deploy/deviy-controller.service  systemd 服务示例
deploy/nginx.example.conf        HTTPS 反向代理与 UI 注入示例
scripts/verify-artifact.py       校验程序 SHA-256
CHECKSUMS.sha256                 归档程序的哈希
README.md                       本说明
~~~

原站点路径为 /www/wwwroot/demovps.deviy.cn；示例沿用该路径，使用其他安装目录时同时修改 systemd、Nginx 和文件权限。

## 四、环境要求与程序校验

- Linux x86-64。
- systemd 或其他可靠进程管理器。
- Nginx；对外使用有效 HTTPS 证书。
- 到虚拟化节点或 Agent 的网络连通性。
- 具有读写状态文件权限的独立运行账户。
- Python 3 仅用于仓库提供的哈希校验脚本，不是主控运行依赖。

克隆并校验：

~~~bash
git clone https://github.com/WilliamBacteria/deviy-vps-controller.git
cd deviy-vps-controller
python3 scripts/verify-artifact.py
chmod 755 bin/deviy-controller
./bin/deviy-controller --help
~~~

SHA-256 用于检测归档内容变化，不等于供应链签名或恶意代码审计。程序来自用户授权管理的现网部署，本仓库没有修改程序字节。

## 五、全新环境部署

1. 安装可执行程序和 UI：

~~~bash
install -d -m 755 /www/wwwroot/demovps.deviy.cn
install -m 755 bin/deviy-controller /www/wwwroot/demovps.deviy.cn/deviy-controller
cp -R ui-azure /www/wwwroot/demovps.deviy.cn/
~~~

2. 新环境创建空状态。**恢复现网时不要执行这一步覆盖原状态**：

~~~bash
cp deploy/controller-state.example.json /www/wwwroot/demovps.deviy.cn/controller-state.json
chown www:www /www/wwwroot/demovps.deviy.cn/controller-state.json
chmod 600 /www/wwwroot/demovps.deviy.cn/controller-state.json
~~~

3. 创建独立机密配置：

~~~bash
install -d -m 700 /etc/deviy-controller
install -m 600 deploy/controller.env.example /etc/deviy-controller/controller.env
~~~

编辑该文件，设置 DEVIY_ADMIN_USER、DEVIY_ADMIN_PASSWORD 和 DEVIY_API_TOKEN；保留真实值在服务器中，不提交仓库。新的随机 Token 还须在财务站配置为相同值。

4. 修改 deploy/deviy-controller.service 中的 www 用户、组和路径，使其与服务器匹配，安装后启动：

~~~bash
cp deploy/deviy-controller.service /etc/systemd/system/deviy-controller.service
systemctl daemon-reload
systemctl enable --now deviy-controller
systemctl status deviy-controller
~~~

程序默认仅监听回环地址，外部访问通过 HTTPS Nginx。直接启动用于故障排查时，需要自行以相同权限加载环境配置，不要在 shell 历史记录里输入完整密码或 Token。

## 六、程序命令行参数

以归档程序 --help 输出为依据：

| 参数 | 说明 |
| --- | --- |
| --listen | HTTP/API 监听地址；示例使用 127.0.0.1:18088 |
| --state | 状态 JSON 文件路径 |
| --nodes | JSON 节点数组；恢复环境优先保留原状态配置 |
| --admin-user、--admin-password | Web 管理认证；现网使用 DEVIY_ADMIN_USER / DEVIY_ADMIN_PASSWORD 环境变量 |
| --api-token | 机器接口 Bearer Token；现网使用 DEVIY_API_TOKEN 环境变量 |
| --allow-register | 允许节点注册；默认不启用 |
| --register-token | 节点注册 Token |
| --agent-ca、--agent-cert、--agent-key | 主控到 Agent 的 mTLS 证书配置 |
| --listen-cert、--listen-key | 主控自身 HTTPS 证书 |
| --client-ca | 要求接入主控 HTTPS 的客户端证书 |

不要复制现网节点 Token 或私钥进 README、脚本或命令行示例。Agent 注册应按实际部署管理，不能把公开注册作为默认配置。

## 七、Nginx 与 UI 注入

原程序内嵌页面，ui-azure 通过 Nginx sub_filter 注入：

- /__azure/ 使用 alias 指向 ui-azure。
- theme-init.js 在 head 注入，尽早读取主题。
- azure.css、theme.css、azure.js、theme.js 在 body 结束前加载。
- 关闭发往上游的 Accept-Encoding，保证 HTML 可以被 sub_filter 修改。
- WebSocket Upgrade 保留，用于长连接场景。
- 生产入口使用 HTTPS；示例证书路径必须替换。

参考 deploy/nginx.example.conf。检查 nginx -t 成功后再 reload。宝塔环境在对应站点的配置里合并，不要同时复制出重复 server 块或覆盖面板证书配置。

只改 UI 时无需重编译 Go 程序；更新静态文件并同步调整注入 URL 的版本参数即可。

## 八、财务侧接入

财务项目使用 config/deviy.php 中的：

- DEVIY_CONTROLLER_URL：后端访问地址，同机部署可用 http://127.0.0.1:18088。
- DEVIY_CONTROLLER_PUBLIC_URL：外部可访问的 HTTPS 地址。
- DEVIY_CONTROLLER_TOKEN：与主控 API Token 一致。
- DEVIY_CONTROLLER_CA / CERT / KEY：按实际 mTLS 使用。
- DEVIY_CONTROLLER_STATE_PATH：确需共享状态时按现有设计配置，不能任意指向其他站点数据。

接口调用规则以财务仓库 app/Services/Provisioning 的实现及主控页面为准。本次没有从二进制推导一份未经验证的 API 文档。

## 九、状态、备份与恢复

生产 controller-state.json 至少包含：

- nodes：节点 ID、地址、令牌、目录、桥接和端口配置。
- images：镜像、模板路径和节点对应关系。
- ip_pools：地址段、网关、类型与分配记录。
- vm_allocations：虚拟机到地址分配的绑定。

这些数据不在 GitHub。备份时需考虑程序仍在写入，采用一致性快照或在维护窗口停止写入后备份。备份文件加密保存，并限制访问。

恢复必须同时核对：

1. 程序哈希和 UI 版本。
2. 原状态文件、管理员与 API 配置。
3. 节点 Agent、Token、证书及网络连通性。
4. IP 池、虚拟机 ID 和财务资源关联。
5. 镜像、磁盘、桥接和端口配置。
6. Nginx 与域名证书。

**不要用空模板覆盖现有状态，也不要启动两份主控共同写同一个状态文件。** 没有 Agent 和实际节点，恢复此仓库不能恢复客户的磁盘或虚拟机。

## 十、更新、排障与回滚

- 修改 UI 前记录 Git 提交号。
- 替换可执行文件前备份程序、状态、机密配置，核对兼容性。
- 回滚程序不一定回滚状态格式，先确认新旧版本的数据兼容。
- 认证失败：核对环境文件、systemd 配置和财务 Token，避免把真实值输出日志。
- UI 没加载：检查 /__azure/ 路径、Nginx sub_filter、压缩、缓存与资源版本。
- 节点离线：核对节点服务、地址、证书和网络，不以修改前端状态代替实际恢复。
- 无法写状态：核对运行账户、文件所有者与父目录权限。
- 502：检查 systemctl status deviy-controller 与监听端口。
- 日志：journalctl -u deviy-controller，另查 Nginx error log；对外分享前去除敏感内容。

## 十一、许可与维护

该私有归档不声明对二进制或第三方资源的开源授权。Noto 字体许可保留于对应资源目录。

后续获取 Go 源码后，应增加 go.mod / go.sum、版本标记、构建校验、测试与发布说明；在此之前，不要把本仓库描述为可完整重编译的主控源码。
