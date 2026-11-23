# Windows 客户机工具

本目录保存来自原工程的 Guest Agent 与 VirtIO 安装/验证脚本。二进制和第三方驱动安装包不纳入源码仓库。

## Guest Agent 安装

1. 从仓库根目录进入 agent，构建 Windows amd64 的 cmd/deviy-guest-agent。
2. 将结果命名为 deviy-guest-agent.exe，复制到本目录的安装脚本旁。
3. 在测试 VM 以管理员身份运行 Install-DeviyGuestAgent.ps1。
4. 根据实际宿主机网桥地址设置 AllowedCIDR，并提供独立随机 Token。
5. 确认 8099 仅允许指定宿主机管理来源访问。

示例参数格式：

~~~powershell
.\Install-DeviyGuestAgent.ps1 -AllowedCIDR '192.168.122.1/32' -Token '替换为至少24字符的独立随机Token'
~~~

安装脚本会复制程序、配置来源受限的防火墙规则并注册 SYSTEM 计划任务。卸载或修改规则前先检查实际服务状态。

## VirtIO

Optimize-DeviyWindows.ps1 与 Verify-DeviyVirtio.ps1 是原工程的驱动工具脚本。这里不附带 virtio-win-guest-tools.exe；需要使用时另行从可信官方来源取得并核对版本。脚本中的文件名不会自动提供相应安装包。

这些脚本和 Go Agent 功能不同，安装驱动不等于已安装 Deviy Guest Agent。当前代码与实际系统版本兼容性须在测试 VM 验证。
