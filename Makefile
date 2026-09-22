# CodeBuddy2API 用户交付构建
#
# 运行 `make dist` 后，dist/ 包含三件核心交付物和两个可选启动脚本：
#   1. codebuddy-gateway(.exe)     可执行产物
#   2. config.yaml                 只需修改必要项的配置
#   3. config.reference.yaml       完整配置参考
#   4. stop.ps1                    Windows 终止脚本
#   5. start-on-login.vbs          Windows 开机/登录自启动脚本
#
# 用户不需要安装 Go、gcc 或其它运行时依赖；这些只在从源码构建时需要。

APP       ?= codebuddy-gateway
DIST      ?= dist
GO        ?= go
GOOS      ?= $(shell $(GO) env GOOS)
GOARCH    ?= $(shell $(GO) env GOARCH)
VERSION   ?= dev
CGO_ENABLED ?= 1
GOFLAGS   ?= -buildvcs=false -trimpath
LDFLAGS   ?= -s -w -X main.Version=$(VERSION)

ifeq ($(GOOS),windows)
BINARY := $(DIST)/$(APP).exe
else
BINARY := $(DIST)/$(APP)
endif

.PHONY: all dist build test fmt clean help

all: dist

help:
	@echo "make dist  - 构建用户交付目录（只含产物、配置、配置参考）"
	@echo "make test  - 运行全部 Go 测试"
	@echo "make clean - 删除 dist/"
	@echo ""
	@echo "可选参数：GOOS=windows|linux|darwin GOARCH=amd64|arm64 VERSION=vX.Y.Z"

# 先清空再复制，确保 dist 永远不会残留旧版本或敏感文件。
dist:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "if (Test-Path -LiteralPath '$(DIST)') { Remove-Item -LiteralPath '$(DIST)' -Recurse -Force }; New-Item -ItemType Directory -Force -Path '$(DIST)' | Out-Null"
else
	@rm -rf "$(DIST)" && mkdir -p "$(DIST)"
endif
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "$$env:CGO_ENABLED = '$(CGO_ENABLED)'; $$env:GOOS = '$(GOOS)'; $$env:GOARCH = '$(GOARCH)'; & '$(GO)' build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o '$(BINARY)' ."
else
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o "$(BINARY)" .
endif
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "Copy-Item -LiteralPath 'config.dist.yaml' -Destination '$(DIST)/config.yaml' -Force; Copy-Item -LiteralPath 'config.yaml.example' -Destination '$(DIST)/config.reference.yaml' -Force"
ifeq ($(GOOS),windows)
	@powershell -NoProfile -Command "Copy-Item -LiteralPath 'stop.ps1' -Destination '$(DIST)/stop.ps1' -Force; Copy-Item -LiteralPath 'start-on-login.vbs' -Destination '$(DIST)/start-on-login.vbs' -Force"
endif
else
	@cp config.dist.yaml "$(DIST)/config.yaml" && cp config.yaml.example "$(DIST)/config.reference.yaml"
endif
ifeq ($(GOOS),windows)
	@echo "已构建：$(DIST)/（核心产物 + 配置 + Windows 启停脚本）"
else
	@echo "已构建：$(DIST)/（核心产物 + 配置）"
endif

# 兼容习惯用法：源码编译仍然落到 dist，避免根目录生成容易误用的旧二进制。
build: dist

test:
	@$(GO) test ./...

fmt:
	@$(GO) fmt ./...

clean:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "if (Test-Path -LiteralPath '$(DIST)') { Remove-Item -LiteralPath '$(DIST)' -Recurse -Force }"
else
	@rm -rf "$(DIST)"
endif
