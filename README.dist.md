# CodeBuddy2API 快速上手
把腾讯 CodeBuddy 的登录态转换成本地 OpenAI 兼容网关，供 Codex、Claude Code、
Cherry Studio、LobeChat、Open WebUI 等客户端使用。运行时不需要安装 Go、gcc、
Node.js、Python 或数据库。
## 目录内容
```text
codebuddy-gateway.exe       # 可执行文件（Windows）
codebuddy-gateway           # 命令行可执行文件（macOS/Linux）
CodeBuddy2API.app           # Finder 双击启动应用（仅 macOS 包）
config.yaml                 # 日常配置，只需要改必要项
config.reference.yaml       # 完整配置参考，通常不用改
README.md                   # 本文件
stop.ps1                    # 停止后台服务（仅 Windows 包）
start-on-login.vbs          # 登录后自动启动（仅 Windows 包）
stop.command                # 停止服务（仅 macOS 包）
start-on-login.command      # 安装/卸载登录后自启动（仅 macOS 包）
```
`data/` 和 `log/` 由程序在首次运行时自动创建，属于运行数据，不属于发布包。
## 三步启动
1. 用记事本打开 `config.yaml`，至少修改两个 Key：
   ```yaml
   gateway:
     api-key: "sk-your-local-api-key"
   ```
   - `api-key`：下游客户端访问 `/v1/*` 使用的 Key。
   - 这个 Key 只在本机网关使用，不是 CodeBuddy 的账号密码。
2. 启动程序：
   - Windows：双击 `codebuddy-gateway.exe`。
   - macOS：在 Finder 中双击 `CodeBuddy2API.app`，体验与 Windows 双击一致；也可在终端执行 `./codebuddy-gateway`。
   - Linux：在终端执行 `./codebuddy-gateway server`。
   - 首次运行或没有登录账号：程序会自动打开浏览器完成 CodeBuddy 登录。
   - 已经登录过：直接后台启动服务。
3. 服务只提供 `/v1/*` 和 `/healthz`；浏览器控制台已移除，账号管理请使用命令行。
> macOS / Linux 下载后若无执行权限，先执行 `chmod +x ./codebuddy-gateway`。
> macOS 首次运行可能提示无法验证开发者：在 Finder 中按住 Control 点击 `CodeBuddy2API.app`，选择「打开」，
> 再确认打开；也可在终端执行 `xattr -dr com.apple.quarantine CodeBuddy2API.app`。
> 当前应用未做 Apple 开发者签名与公证，因此首次启动需要此确认。
启动后客户端接入地址：
| 客户端类型 | Base URL | API Key |
|---|---|---|
| OpenAI / Codex / Cherry Studio | `http://127.0.0.1:8088/v1` | `gateway.api-key`；免密时可留空 |
| Claude Code / Anthropic 客户端 | `http://127.0.0.1:8088` | `gateway.api-key`；免密时可留空 |
## Codex 客户端配置
把下面内容写入 Codex 的 `~/.codex/config.toml`（Windows 为
`C:\Users\<你的用户名>\.codex\config.toml`），即可让 Codex 使用本地网关：
```toml
model_provider = "custom"
model = "deepseek-v4.1-flash"
model_reasoning_effort = "high"
model_context_window = 1000000
model_auto_compact_token_limit = 900000
[model_providers.custom]
name = "custom"
wire_api = "responses"
requires_openai_auth = false
base_url = "http://127.0.0.1:8088/v1"
experimental_bearer_token = "sk-change-me"
```
需要按自己的网关配置调整的地方：
- `experimental_bearer_token`：改成 `config.yaml` 里的 `gateway.api-key`；
  如果开启了免密模式，可以留空。
- `model`：改成你要用的模型名，可用 `http://127.0.0.1:8088/v1/models` 查看当前可选模型。
- `model_context_window` / `model_auto_compact_token_limit`：按模型实际上下文调整；
  上面的 100 万是示例值，网关不会按本地模型目录截断请求。
改完后重启 Codex，新会话即可走本地网关。
> CodeBuddy 上游使用 Chat Completions，不执行 Responses hosted tools。Codex 请求中的 `web_search`、`file_search` 等工具会被安全降级并记录 warning；function、custom 和 MCP 工具仍会转换后执行。
## 常用命令
Windows（PowerShell）：
```powershell
.\codebuddy-gateway.exe server        # 前台启动；默认日志写入 log/
.\codebuddy-gateway.exe auth login    # 手动触发登录
.\codebuddy-gateway.exe account list  # 查看已导入账号
.\stop.ps1                            # 停止后台服务
```
macOS / Linux：
```bash
./codebuddy-gateway server             # 前台启动；默认日志写入 log/
./codebuddy-gateway auth login         # 手动触发登录
./codebuddy-gateway auth login --no-browser
./codebuddy-gateway account list       # 查看已导入账号
./codebuddy-gateway account import-desktop
./codebuddy-gateway account task batch      # 为所有启用账号执行积分任务
```
macOS 在 Finder 双击 `CodeBuddy2API.app` 会像 Windows 一样自动检查账号：首次使用自动打开浏览器登录，
随后网关在后台运行；再次双击会提示已运行或启动成功。双击 `stop.command` 可安全停止当前发行目录的网关。
双击 `start-on-login.command` 可安装登录后自启动；卸载可在终端运行 `./start-on-login.command uninstall`。
如果通过 LaunchAgent 自启动，请勿移动发行目录；移动后重新运行安装脚本。Linux 停止前台服务按 Ctrl+C，后台服务请用对应的服务管理器停止。
默认监听 `127.0.0.1:8088`，仅本机可访问。如需局域网访问，把 `system.listenAddr`
改为 `0.0.0.0:8088`，并务必关闭免密模式、设置足够复杂的 Key。
## 日志
日志默认写入发行目录的 `log/`，按天切割、保留 30 天；级别由 `config.yaml` 的 `zap` 段控制：
```yaml
zap:
  level: info           # debug / info / warn / error，越大越安静
  director: log         # 日志目录
  retention-day: 30     # 保留天数
  log-in-console: false # 后台服务默认不刷控制台；前台调试可改 true
```
- `info`（默认）：启动、每个请求、选号与账号状态变化、看门狗/凭证刷新结果。
- `warn`：只在异常时输出（上游报错、账号被冷却、认证失败等），日志最干净。
- `error`：只保留真正的错误。
- `debug`：额外打印每次上游请求/响应、选号细节，排障时用；量很大，平时不要开。
也可以不改文件，用命令行或环境变量临时覆盖：
```powershell
.\codebuddy-gateway.exe server --log-level debug
$env:GATEWAY_LOG_LEVEL = "warn"; .\codebuddy-gateway.exe server
```
## 遇到问题
- 启动没反应：Windows 双击无窗口时查看 `log/desktop-login.log`；
  服务日志默认写入 `log/` 目录；需要在终端同步查看时，将 `zap.log-in-console` 设为 `true`。
- 想临时免密：把 `passwordless.enabled` 改为 `true`，仅对 `/v1/*` 生效，
  服务不再提供控制台。
- 所有配置项、代理、模型别名、多账号策略等说明见 `config.reference.yaml`。
账号、令牌和用量都保存在当前目录的 `data/gateway.db`，请妥善保管整个目录，
也不要把它提交到 Git。