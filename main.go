package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"codebuddy-gateway/cli"
	"codebuddy-gateway/global"
)

var Version = "v1.0.0"
var AppName = "CodeBuddyGateway"

func main() {
	global.CORE_APP_NAME = AppName
	global.CORE_APP_VERSION = Version

	// Windows / macOS 直接双击程序时进入桌面用户体验：自动登录（首次使用）
	// 或启动后台服务；带命令参数启动时仍保持完整 CLI 行为。
	desktopLaunch := len(os.Args) == 1
	// LaunchServices may append a process-serial-number argument when opening an
	// application bundle; treat that Finder launch like a no-argument double-click.
	if runtime.GOOS == "darwin" && len(os.Args) == 2 && strings.HasPrefix(os.Args[1], "-psn_") {
		desktopLaunch = true
	}
	if desktopLaunch && cli.RunDesktop() {
		return
	}

	fmt.Println("CodeBuddy Gateway\n Version: ", Version)
	cli.Execute()
}
