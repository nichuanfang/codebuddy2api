# CodeBuddy2API

把腾讯 CodeBuddy 的登录态转换成 OpenAI 兼容接口，供 Codex、Claude Code、Cherry Studio、LobeChat、Open WebUI 等客户端使用。

> **面向用户的目标**：下载、解压、改两行配置、启动。运行时不需要安装 Go、gcc、Node.js、Python 或数据库。

## 下载后无需安装依赖

每个发布包由 `make dist` 生成，核心是四件文件；Windows 包额外带两个便捷脚本：

```text
codebuddy-gateway.exe       # Windows 可执行文件
codebuddy-gateway           # macOS / Linux 可执行文件
config.yaml                 # 日常配置：只改必要项
config.reference.yaml       # 完整配置参考，通常不用改
README.md                   # 简短上手说明
stop.ps1                    # 终止后台服务（仅 Windows 包）
start-on-login.vbs          # 适合放进 Windows 启动目录（仅 Windows 包）
```

运行时不需要安装 Go、gcc、Node.js、Python 或数据库。程序会在当前目录自动创建 `data/` 和 `log/`，它们是运行数据，不属于发布包。

## Windows 用户：直接双击即可

1. 用记事本打开 `config.yaml`，至少修改：

   ```yaml
   gateway:
     api-key: "sk-your-local-api-key"
     admin-key: "sk-your-admin-key"
    # 上游 SSE 连续静默超过此时长则终止流；每收到数据都会重置。
    stream-idle-timeout-seconds: 300
   ```

   - `api-key`：下游 AI 客户端访问 `/v1/*` 使用的 Key。
   - `admin-key`：登录控制台和调用 `/admin/*` 使用的 Key。
   - 这两个 Key 只在本机网关使用，不是 CodeBuddy 账号密码。

2. 双击 `codebuddy-gateway.exe`：

   - **首次运行 / 没有登录账号**：程序会提示即将打开登录页面，自动打开浏览器；完成 CodeBuddy 登录后会提示“登录成功”，随后自动启动后台服务。
   - **已经登录过**：程序会直接启动后台服务，弹出启动成功提示，2 秒后自动关闭。
   - 程序和提示窗口都不会留下需要手动关闭的命令行窗口。

3. 浏览器打开 `http://127.0.0.1:8088/`，用 `admin-key` 登录控制台。

如果双击后没有反应，查看当前目录的 `log/desktop-login.log`、`log/gateway.stderr.log`。

### 手动命令行启动

需要查看日志或调试时仍可使用：

```powershell
.\codebuddy-gateway.exe server
```

Linux/macOS：

```bash
chmod +x ./codebuddy-gateway
./codebuddy-gateway server
```

### macOS 用户

macOS 发布包提供 Intel（x64）与 Apple 芯片（arm64）两个版本，按机器架构下载对应压缩包。
解压后进入目录，首次运行先赋予执行权限：

```bash
chmod +x ./codebuddy-gateway
./codebuddy-gateway
```

首次运行会通过 `open` 自动打开浏览器完成 CodeBuddy 登录；已完成登录后再次启动会直接作为服务运行。
如果 macOS 提示「无法打开，因为 Apple 无法检查是否包含恶意软件」，可在「系统设置 → 隐私与安全性」
中点击「仍要打开」，或执行 `xattr -d com.apple.quarantine ./codebuddy-gateway` 移除隔离标记。

停止服务：前台运行时按 `Ctrl+C`；后台运行时用 `pkill -f codebuddy-gateway`。

`stop.ps1` 与 `start-on-login.vbs` 仅适用于 Windows。macOS 如需开机自启，可自行编写
`launchd` plist；登录态与账号数据同样保存在当前目录的 `data/gateway.db`。

默认监听 `127.0.0.1:8088`，只允许本机访问；如需局域网访问，再显式改为 `0.0.0.0:8088`，并关闭免密。


## 停止服务与开机启动

### 停止后台服务

在程序目录打开 PowerShell：

```powershell
.\stop.ps1
```

脚本只会终止当前目录下的 `codebuddy-gateway.exe`，不会误杀其它同名程序。

### 设置 Windows 登录后自动启动

1. 按 `Win + R`，输入 `shell:startup` 并回车。
2. 将 `start-on-login.vbs` 或它的快捷方式放入打开的启动目录；也可以直接复制脚本。
3. 下次登录 Windows 后，脚本会隐藏启动网关：有登录态则直接运行，没有登录态则打开 CodeBuddy 登录流程。

如需取消开机启动，从启动目录中删除该脚本或快捷方式即可。

## 免密模式

如果下游客户端支持免密但不方便填写 API Key，可以在 `config.yaml` 中改成：

```yaml
system:
  listenAddr: "127.0.0.1:8088"

passwordless:
  enabled: true
```

免密只对 `/v1/*` 生效；控制台和 `/admin/*` **仍然必须使用 `gateway.admin-key`**。不要在公网或不可信局域网监听免密服务。

也可以不改文件，使用环境变量临时开启：

```powershell
$env:GATEWAY_PASSWORDLESS = "true"
.\codebuddy-gateway.exe server
```

## 客户端接入

服务启动后使用以下设置：

| 客户端类型 | Base URL | API Key |
|---|---|---|
| OpenAI / Codex / Cherry Studio | `http://127.0.0.1:8088/v1` | `gateway.api-key`；免密时可留空 |
| Claude Code / Anthropic 客户端 | `http://127.0.0.1:8088` | `gateway.api-key`；免密时可留空 |

支持：

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/responses/compact`
- `POST /v1/messages`
- `GET /v1/models`
- `GET /healthz`

`/v1/models` 优先读取 CodeBuddy 实时模型目录，并缓存 5 分钟；上游不可用时回退到本地目录。常见旧模型名可通过 `gateway.model-alias` 映射到当前模型。

`/v1/messages/count_tokens` 默认使用本地估算。如需读取上游真实 usage，可设置 `gateway.count-tokens-mode: upstream`；该模式会发起一次最小生成请求，可能消耗上游额度。

### Codex 上下文窗口

上下文窗口由 Codex 客户端的 `model_context_window` 与 `model_auto_compact_token_limit` 决定；Responses 请求本身不会携带这两个配置值。网关不会按本地模型目录中的 `max_input` 截断请求，而是完整转发 Codex 已组装的上下文。例如 1M 上下文可在 Codex 的 `config.toml` 中配置：

```toml
model_context_window = 1000000
model_auto_compact_token_limit = 900000
```

`max_input` 目前只用于控制台模型信息展示，不是网关侧硬限制。最终可用上下文仍受实际 CodeBuddy 上游模型限制；如果上游拒绝超长请求，网关会保留上游错误而不会静默截断历史。

`/v1/responses/compact` 会调用上游生成会话摘要，并返回 `response.compaction`。由于 CodeBuddy 上游不是 OpenAI 原生 Responses 服务，网关使用自身的不透明摘要封装保存 compact 内容；后续由同一网关接收时可以继续还原，不能与其它网关实例互换。`previous_response_id` 仅支持当前网关进程内已生成的 compact response。

## 登录与账号

官方登录命令会打开 CodeBuddy 登录页，并把登录态保存到本地 SQLite：

```powershell
.\codebuddy-gateway.exe auth login
```

如果当前环境不能自动打开浏览器：

```powershell
.\codebuddy-gateway.exe auth login --no-browser
```

也支持导入 CodeBuddy / WorkBuddy 导出的 JSON，或直接导入桌面端已有登录态：

```powershell
.\codebuddy-gateway.exe account import .\accounts.json
.\codebuddy-gateway.exe account import-desktop
.\codebuddy-gateway.exe account list
```

`account import-desktop` 会自动查找 CodeBuddy/WorkBuddy 的本地 `*.info` 登录快照，导入后只写入网关自己的 SQLite，不会回写桌面端文件。也可以使用 `--path` 指定 auth 目录，使用 `--dry-run` 只解析不入库。

账号、刷新令牌、额度和用量只保存在当前目录的 `data/gateway.db`，请妥善保护整个目录。

## 面向 CodeBuddy 的兼容处理

当前实现针对 CodeBuddy 的实际接口做了以下兼容：

- 使用 CodeBuddy 的插件登录、Token 轮询与刷新接口；登录中的 `10008` 和 `11217` 会继续轮询。
- 使用 `/v3/config` 获取实时模型目录，不依赖长期维护的硬编码模型清单。
- 支持 Codex Responses、Anthropic Messages、OpenAI Chat Completions 三种协议，并转换工具调用、流式响应、reasoning 和常见多模态内容。
- CodeBuddy 上游不执行 Responses hosted tools；`web_search`、`file_search` 等会被安全降级并记录 warning，function/custom/MCP 工具仍会正常转换。
- 对 Codex / Agent harness 的 system、developer、工具描述和历史上下文做出站清洗，降低上游策略误判；遇到 `11128` 会自动用更严格模式重试一次。
- 多账号支持 sticky、least_used、round_robin；结合额度、失败冷却和实测 `usage.credit` 选择账号。
- 上游 HTTP 连接复用、HTTP/2、长流式响应和 gzip 请求均已启用；流式响应不使用容易截断长回答的总超时。
- SSE 流会检测正常结束标志、提前 EOF、解析错误和上游静默；异常时返回协议级失败事件，不会把截断内容伪装成成功。

## 配置原则

日常只需要修改 `config.yaml` 中的：

```yaml
gateway:
  api-key: "sk-your-local-api-key"
  admin-key: "sk-your-admin-key"

system:
  listenAddr: "127.0.0.1:8088"

passwordless:
  enabled: false
```

完整字段、代理、模型别名、刷新、看门狗、数据库和控制台设置见 `config.reference.yaml`。SQLite 是默认值，不需要额外安装数据库。

配置也可以由环境变量覆盖：

```text
GATEWAY_API_KEY       覆盖 gateway.api-key
GATEWAY_ADMIN_KEY     覆盖 gateway.admin-key
GATEWAY_LISTEN        覆盖 system.listenAddr
GATEWAY_PASSWORDLESS  覆盖 passwordless.enabled
```

优先级：命令行参数 > 环境变量 / `.env` > `config.yaml`。

## 从源码构建

普通用户不需要执行本节。开发者需要 Go 1.25+、gcc（SQLite 使用 CGO）和 make：

```bash
# Linux/macOS
make test
make dist

# Windows（MinGW Make）
mingw32-make test
mingw32-make dist
```

`make dist` 会先清空 `dist/`，然后重新生成核心产物、配置和两个 Windows 便捷脚本，不会把 `.env`、数据库、日志或源码复制进去。

可指定目标平台：

```bash
make dist GOOS=linux GOARCH=amd64 VERSION=v1.0.0
make dist GOOS=windows GOARCH=amd64 VERSION=v1.0.0
make dist GOOS=darwin GOARCH=amd64 VERSION=v1.0.0   # macOS Intel
make dist GOOS=darwin GOARCH=arm64 VERSION=v1.0.0   # macOS Apple 芯片
```

注意 macOS 目标需要在 macOS 上构建：SQLite 依赖 CGO，Windows/Linux 上缺少 macOS 交叉编译工具链，
本地无法直接产出可用的 darwin 二进制（CI 使用 macOS runner 解决这一点）。

## 手动发布

仓库提供 `.github/workflows/release.yml`。在 GitHub Actions 中手动运行 **Release**，填写版本号（例如 `v1.0.0`），工作流会：

1. 分别通过 Makefile 构建 Windows x64、Linux x64、macOS x64、macOS arm64 四类产物；
2. 各压缩包包含可执行文件、两个配置文件和 `README.md`；Windows 包另含 `stop.ps1` 与 `start-on-login.vbs`；
3. 创建或更新对应 GitHub Release，并上传压缩包。

macOS 产物必须在 macOS runner 上原生构建：SQLite 依赖 CGO，无法从 Linux/Windows 交叉编译。
工作流使用 `macos-15-intel`（Intel x64）与 `macos-15`（Apple 芯片 arm64）两个免费托管 runner。

## 安全提醒

- 不要把真实 `config.yaml`、`.env`、`data/`、日志或导出的账号 JSON 提交到 Git。
- `admin-key` 和登录态等同于本地管理凭据。
- 免密只适用于可信的本机回环地址。
- 如果必须让局域网访问，请关闭免密并设置足够复杂的 `api-key`、`admin-key`。

## 许可证

本项目按仓库中的许可证发布。
