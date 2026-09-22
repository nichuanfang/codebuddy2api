# CodeBuddy2API

把腾讯 CodeBuddy 的登录态转换成 OpenAI 兼容接口，供 Codex、Claude Code、Cherry Studio、LobeChat、Open WebUI 等客户端使用。

> **面向用户的目标**：下载、解压、改两行配置、启动。运行时不需要安装 Go、gcc、Node.js、Python 或数据库。

## 下载后只有三件东西

每个发布包由 `make dist` 生成，并且只包含：

```text
codebuddy-gateway.exe       # Windows 可执行文件（Linux/macOS 为对应无扩展名文件）
config.yaml                 # 日常配置：只改必要项
config.reference.yaml       # 完整配置参考，通常不用改
```

程序会在当前目录自动创建 `data/` 和 `log/`。它们是运行数据，不属于发布包。

## 3 步启动

1. 用记事本打开 `config.yaml`，至少修改：

   ```yaml
   gateway:
     api-key: "sk-your-local-api-key"
     admin-key: "sk-your-admin-key"
   ```

   - `api-key`：下游 AI 客户端访问 `/v1/*` 使用的 Key。
   - `admin-key`：登录控制台和调用 `/admin/*` 使用的 Key。
   - 这两个 Key 只在本机网关使用，不是 CodeBuddy 账号密码。

2. 启动：

   ```powershell
   .\codebuddy-gateway.exe server
   ```

   Linux/macOS：

   ```bash
   chmod +x ./codebuddy-gateway
   ./codebuddy-gateway server
   ```

3. 浏览器打开 `http://127.0.0.1:8088/`，用 `admin-key` 登录，然后按页面提示完成 CodeBuddy 登录。

默认监听 `127.0.0.1:8088`，只允许本机访问；如需局域网访问，再显式改为 `0.0.0.0:8088`，并关闭免密。

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
- `POST /v1/messages`
- `GET /v1/models`
- `GET /healthz`

`/v1/models` 优先读取 CodeBuddy 实时模型目录，并缓存 5 分钟；上游不可用时回退到本地目录。常见旧模型名可通过 `gateway.model-alias` 映射到当前模型。

## 登录与账号

官方登录命令会打开 CodeBuddy 登录页，并把登录态保存到本地 SQLite：

```powershell
.\codebuddy-gateway.exe auth login
```

如果当前环境不能自动打开浏览器：

```powershell
.\codebuddy-gateway.exe auth login --no-browser
```

也支持导入 CodeBuddy / WorkBuddy 导出的 JSON：

```powershell
.\codebuddy-gateway.exe account import .\accounts.json
.\codebuddy-gateway.exe account list
```

账号、刷新令牌、额度和用量只保存在当前目录的 `data/gateway.db`，请妥善保护整个目录。

## 面向 CodeBuddy 的兼容处理

当前实现针对 CodeBuddy 的实际接口做了以下兼容：

- 使用 CodeBuddy 的插件登录、Token 轮询与刷新接口；登录中的 `10008` 和 `11217` 会继续轮询。
- 使用 `/v3/config` 获取实时模型目录，不依赖长期维护的硬编码模型清单。
- 支持 Codex Responses、Anthropic Messages、OpenAI Chat Completions 三种协议，并转换工具调用、流式响应、reasoning 和常见多模态内容。
- 对 Codex / Agent harness 的 system、developer、工具描述和历史上下文做出站清洗，降低上游策略误判；遇到 `11128` 会自动用更严格模式重试一次。
- 多账号支持 sticky、least_used、round_robin；结合额度、失败冷却和实测 `usage.credit` 选择账号。
- 上游 HTTP 连接复用、HTTP/2、长流式响应和 gzip 请求均已启用；流式响应不使用容易截断长回答的总超时。

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

`make dist` 会先清空 `dist/`，然后重新生成且只保留上面所列的三件东西，不会把 `.env`、数据库、日志或源码复制进去。

可指定目标平台：

```bash
make dist GOOS=linux GOARCH=amd64 VERSION=v1.0.0
make dist GOOS=windows GOARCH=amd64 VERSION=v1.0.0
```

## 手动发布

仓库提供 `.github/workflows/release.yml`。在 GitHub Actions 中手动运行 **Release**，填写版本号（例如 `v1.0.0`），工作流会：

1. 分别通过 Makefile 构建 Windows x64、Linux x64 产物；
2. 每个压缩包只放可执行文件、`config.yaml`、`config.reference.yaml`；
3. 创建或更新对应 GitHub Release，并上传压缩包。

## 安全提醒

- 不要把真实 `config.yaml`、`.env`、`data/`、日志或导出的账号 JSON 提交到 Git。
- `admin-key` 和登录态等同于本地管理凭据。
- 免密只适用于可信的本机回环地址。
- 如果必须让局域网访问，请关闭免密并设置足够复杂的 `api-key`、`admin-key`。

## 许可证

本项目按仓库中的许可证发布。
