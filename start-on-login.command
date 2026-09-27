#!/bin/bash
# CodeBuddy2API 登录后自启动安装/卸载脚本（macOS：在 Finder 中双击即可运行）
# 双击安装；在终端执行 ./start-on-login.command uninstall 可卸载。

set -u
cd "$(dirname "$0")" || exit 1

ROOT="$(pwd -P)"
APP_EXE="$ROOT/CodeBuddy2API.app/Contents/MacOS/CodeBuddy2API"
RAW_EXE="$ROOT/codebuddy-gateway"
if [ -x "$APP_EXE" ]; then EXE="$APP_EXE"; else EXE="$RAW_EXE"; fi
CONFIG="$ROOT/config.yaml"
PID_FILE="$ROOT/codebuddy-gateway.pid"
LABEL="com.codebuddy2api.gateway"
PLIST="$HOME/Library/LaunchAgents/${LABEL}.plist"
LOG_DIR="$ROOT/log"
DOMAIN="gui/$(id -u)"

notify() {
  local icon="note"
  [ "${2:-}" = "error" ] && icon="caution"
  osascript \
    -e 'on run argv' \
    -e "display dialog (item 1 of argv) with title \"CodeBuddy2API\" buttons {\"好\"} default button 1 with icon ${icon}" \
    -e 'end run' "$1" >/dev/null 2>&1 || true
}

uninstall() {
  launchctl bootout "$DOMAIN/$LABEL" >/dev/null 2>&1 || true
  rm -f "$PLIST"
  notify "已取消 CodeBuddy2API 登录后自动启动。"
  exit 0
}

[ "${1:-}" = "uninstall" ] && uninstall

if [ ! -x "$EXE" ] || [ ! -f "$CONFIG" ]; then
  notify "找不到可执行文件或 config.yaml。请将此脚本与 CodeBuddy2API.app、程序及配置放在同一目录。" error
  exit 1
fi

mkdir -p "$LOG_DIR" "$HOME/Library/LaunchAgents" || {
  notify "无法创建 LaunchAgent 或日志目录。" error
  exit 1
}

xml_escape() {
  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}
EXE_XML=$(xml_escape "$EXE")
ROOT_XML=$(xml_escape "$ROOT")
CONFIG_XML=$(xml_escape "$CONFIG")
PID_XML=$(xml_escape "$PID_FILE")
STDOUT_XML=$(xml_escape "$LOG_DIR/gateway.stdout.log")
STDERR_XML=$(xml_escape "$LOG_DIR/gateway.stderr.log")

cat > "$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${EXE_XML}</string>
    <string>server</string>
    <string>--config</string>
    <string>${CONFIG_XML}</string>
  </array>
  <key>WorkingDirectory</key>
  <string>${ROOT_XML}</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>GATEWAY_PID_FILE</key>
    <string>${PID_XML}</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>${STDOUT_XML}</string>
  <key>StandardErrorPath</key>
  <string>${STDERR_XML}</string>
</dict>
</plist>
PLIST_EOF

if ! plutil -lint "$PLIST" >/dev/null 2>&1; then
  rm -f "$PLIST"
  notify "LaunchAgent 配置文件生成失败。" error
  exit 1
fi

launchctl bootout "$DOMAIN/$LABEL" >/dev/null 2>&1 || true
if ! launchctl bootstrap "$DOMAIN" "$PLIST" >/dev/null 2>&1; then
  notify "登录后自动启动设置失败。请确认当前用户已登录 macOS 桌面后重试。" error
  exit 1
fi

notify "已设置 CodeBuddy2API 登录后自动启动。\n\n卸载：在终端执行\n$0 uninstall"
