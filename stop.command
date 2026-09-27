#!/bin/bash
# CodeBuddy2API 停止脚本（macOS：在 Finder 中双击即可运行）
# 只终止本发行目录中的网关进程，不会误杀其它同名程序。

cd "$(dirname "$0")" || exit 1

APP_NAME="codebuddy-gateway"
ROOT="$(pwd -P)"
PID_FILE="$ROOT/${APP_NAME}.pid"
APP_EXE="$ROOT/CodeBuddy2API.app/Contents/MacOS/CodeBuddy2API"
RAW_EXE="$ROOT/$APP_NAME"

notify() {
  local icon="note"
  [ "${2:-}" = "error" ] && icon="caution"
  osascript \
    -e 'on run argv' \
    -e "display dialog (item 1 of argv) with title \"CodeBuddy2API\" buttons {\"好\"} default button 1 with icon ${icon}" \
    -e 'end run' "$1" >/dev/null 2>&1 || true
}

running_pid() {
  [ -f "$PID_FILE" ] || return 1
  local pid process_path resolved
  pid=$(tr -d '[:space:]' < "$PID_FILE")
  case "$pid" in
    ''|*[!0-9]*) return 1 ;;
  esac

  # macOS 的 ps comm 字段是进程启动时的 argv[0]，可能是相对路径。
  process_path=$(ps -p "$pid" -o comm= 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
  [ -n "$process_path" ] || return 1
  case "$process_path" in
    /*) resolved="$process_path" ;;
    ./*) resolved="$ROOT/${process_path#./}" ;;
    *) resolved="$ROOT/$process_path" ;;
  esac
  resolved="$(cd "$(dirname "$resolved")" 2>/dev/null && pwd -P)/$(basename "$resolved")" || return 1

  if [ "$resolved" = "$RAW_EXE" ] || [ "$resolved" = "$APP_EXE" ]; then
    echo "$pid"
    return 0
  fi
  return 1
}

pid=$(running_pid) || pid=""
if [ -z "$pid" ]; then
  # PID 已失效或已被系统复用；只清除本发行目录的过期 PID 文件。
  if [ -f "$PID_FILE" ]; then rm -f "$PID_FILE"; fi
  notify "CodeBuddy2API 当前没有运行。"
  exit 0
fi

kill "$pid" 2>/dev/null || true
for _ in $(seq 1 30); do
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.2
done
if kill -0 "$pid" 2>/dev/null; then
  # 进程身份在等待期间可能变化，再校验一次后才强制结束。
  if [ "$(running_pid 2>/dev/null)" = "$pid" ]; then kill -9 "$pid" 2>/dev/null || true; fi
fi
rm -f "$PID_FILE"
notify "已停止 CodeBuddy2API，PID=${pid}"
