//go:build darwin

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"codebuddy-gateway/cli/command"
)

// RunDesktop 是 macOS 双击入口，行为对齐 Windows：双击 .app（或从 Finder 打开
// 可执行文件）时先判断是否已在运行、是否需要首次登录，最后后台启动服务并弹出
// 原生提示。返回 true 表示调用方不应继续执行 Cobra。
func RunDesktop() bool {
	exe, err := os.Executable()
	if err != nil {
		notice("CodeBuddy2API", "无法定位程序文件："+err.Error())
		return true
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		notice("CodeBuddy2API", "无法定位程序目录："+err.Error())
		return true
	}
	root := distributionRoot(exe)
	_ = os.Chdir(root)

	configPath := filepath.Join(root, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		notice("CodeBuddy2API", "找不到 config.yaml。请将程序与配置文件放在同一目录。")
		return true
	}

	if isGatewayRunning(root, exe) {
		notice("CodeBuddy2API", "CodeBuddy2API 已在后台运行。\n\n控制台：http://127.0.0.1:8088/")
		return true
	}

	hasAccounts, err := command.HasAccounts(configPath)
	if err != nil {
		notice("CodeBuddy2API", "读取本地登录状态失败：\n"+err.Error())
		return true
	}

	justLoggedIn := false
	if !hasAccounts {
		notice("CodeBuddy2API", "首次使用，需要登录 CodeBuddy。\n\n登录页面即将打开，请在浏览器中完成登录。")
		if err := runLogin(exe, root, configPath); err != nil {
			notice("CodeBuddy2API", "登录未完成。\n\n"+err.Error())
			return true
		}
		justLoggedIn = true
		notice("CodeBuddy2API", "CodeBuddy 登录成功。\n\n网关随后转入后台运行。")
	}

	if err := startServer(exe, root, configPath); err != nil {
		notice("CodeBuddy2API", "服务启动失败：\n"+err.Error())
		return true
	}
	if !justLoggedIn {
		notice("CodeBuddy2API", "启动成功，服务已在后台运行。\n\n控制台：http://127.0.0.1:8088/")
	}
	return true
}

// notice 弹出 macOS 原生对话框；用户点击「好」后返回。用于替代 Windows 的
// MessageBoxTimeoutW —— macOS 没有带超时的 MessageBox，这里保持同样的阻塞语义。
func notice(title, text string) {
	script := fmt.Sprintf("display dialog %s with title %s buttons {\"好\"} default button 1 with icon note",
		quoteAppleScript(text), quoteAppleScript(title))
	cmd := exec.Command("osascript", "-e", script)
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()
}

func quoteAppleScript(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", "").Replace(s) + `"`
}

// distributionRoot returns the directory containing config.yaml and runtime data.
// For a packaged app, the app bundle is placed beside those files in the dist folder.
func distributionRoot(exe string) string {
	for dir := filepath.Dir(exe); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if strings.EqualFold(filepath.Ext(dir), ".app") {
			return filepath.Dir(dir)
		}
	}
	return filepath.Dir(exe)
}

// runLogin 在前台完成一次官方登录，标准输出写入 log/desktop-login.log。
func runLogin(exe, root, configPath string) error {
	logFile, err := openDesktopLog(root, "desktop-login.log")
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "auth", "login", "--config", configPath)
	cmd.Dir = root
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
}

// startServer 把网关作为脱离终端的后台进程启动，父进程退出后仍然存活。
func startServer(exe, root, configPath string) error {
	logFile, err := openDesktopLog(root, "gateway.stdout.log")
	if err != nil {
		return err
	}
	errFile, err := openDesktopLog(root, "gateway.stderr.log")
	if err != nil {
		logFile.Close()
		return err
	}

	cmd := exec.Command(exe, "server", "--config", configPath)
	cmd.Dir = root
	cmd.Stdout = logFile
	cmd.Stderr = errFile
	cmd.Env = append(os.Environ(), "GATEWAY_PID_FILE="+filepath.Join(root, "codebuddy-gateway.pid"))
	// Setsid：脱离父进程会话，避免双击启动的窗口关闭时把服务一起带走。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		errFile.Close()
		return err
	}
	_ = logFile.Close()
	_ = errFile.Close()
	_ = cmd.Process.Release()
	return nil
}

func openDesktopLog(root, name string) (*os.File, error) {
	logDir := filepath.Join(root, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// isGatewayRunning 只在 pid 文件指向的进程确实是当前目录下的网关时才返回 true，
// 避免 pid 复用误判（与 stop.ps1 的路径校验同一思路）。
func isGatewayRunning(root, exe string) bool {
	pidPath := filepath.Join(root, "codebuddy-gateway.pid")
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	running, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	processPath := strings.TrimSpace(string(running))
	knownExecutables := []string{
		filepath.Join(root, "codebuddy-gateway"),
		filepath.Join(root, "CodeBuddy2API.app", "Contents", "MacOS", "CodeBuddy2API"),
		exe,
	}
	for _, known := range knownExecutables {
		if processMatchesExecutable(root, processPath, known) {
			return true
		}
	}
	return false
}

func processMatchesExecutable(root, processPath, exe string) bool {
	if !filepath.IsAbs(processPath) {
		processPath = filepath.Join(root, processPath)
	}
	return filepath.Clean(processPath) == filepath.Clean(exe)
}
