package main

import (
	"fmt"
	"os"

	"codebuddy-gateway/cli"
	"codebuddy-gateway/global"
)

var Version = "v1.0.0"
var AppName = "CodeBuddyGateway"

func main() {
	global.CORE_APP_NAME = AppName
	global.CORE_APP_VERSION = Version

	// Windows 下直接双击程序时进入桌面用户体验：自动登录（首次使用）
	// 或启动后台服务；带参数启动时仍保持完整 CLI 行为。
	if len(os.Args) == 1 && cli.RunDesktop() {
		return
	}

	fmt.Println("CodeBuddy Gateway\n Version: ", Version)
	cli.Execute()
}
