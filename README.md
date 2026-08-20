# Remask

Remask 是一个本地优先的 AI PII 脱敏网关，由独立的核心、二进制分发和桌面项目组成：

- `remask-core`：Go 实现的脱敏引擎、ONNX 推理服务、HTTP/SSE AI API 网关和支持 HTTP/HTTPS、SOCKS5 的选择性解密代理网关。
- `remask-core-dist`：公开的 Core 二进制发行仓库，只通过 Release 资产分发编译产物，不包含 Core 源码。
- `remask-desktop`：公开的 Tauri 桌面客户端源码。桌面构建从 `remask-core-dist` 下载固定版本的 Core 和模型，校验后打包，不需要访问私有 Core 仓库。

仓库拆分和发布供应链见 [docs/repository-split.md](docs/repository-split.md)。

当前技术方案见 [docs/technical-design.md](docs/technical-design.md)。
桌面端 macOS、Windows 和 Linux 的标准开发打包命令见
[docs/packaging.md](docs/packaging.md)。
