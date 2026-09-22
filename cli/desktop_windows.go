//go:build windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"codebuddy-gateway/cli/command"

	"golang.org/x/sys/windows"
)

const (
	messageBoxOK              = 0x00000000
	messageBoxIconInformation = 0x00000040
	messageBoxIconError       = 0x00000010
	messageBoxIconWarning     = 0x00000030
	messageBoxTimeoutMs       = 2000
	showWindowHide            = 0
)

// RunDesktop 是 Windows 双击入口。返回 true 表示调用方不应继续执行 Cobra。
func RunDesktop() bool {
	hideConsoleWindow()

	exe, err := os.Executable()
	if err != nil {
		timedMessage("CodeBuddy2API", "无法定位程序文件："+err.Error(), messageBoxIconError)
		return true
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		timedMessage("CodeBuddy2API", "无法定位程序目录："+err.Error(), messageBoxIconError)
		return true
	}
	root := filepath.Dir(exe)
	_ = os.Chdir(root)
	configPath := filepath.Join(root, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		timedMessage("CodeBuddy2API", "找不到 config.yaml。请将程序与配置文件放在同一目录。", messageBoxIconError)
		return true
	}

	if isGatewayRunning(root, exe) {
		timedMessage("CodeBuddy2API", "CodeBuddy2API 已在后台运行。\n\n控制台：http://127.0.0.1:8088/", messageBoxIconInformation)
		return true
	}

	hasAccounts, err := command.HasAccounts(configPath)
	if err != nil {
		timedMessage("CodeBuddy2API", "读取本地登录状态失败：\n"+err.Error(), messageBoxIconError)
		return true
	}

	justLoggedIn := false
	if !hasAccounts {
		timedMessage("CodeBuddy2API", "首次使用，需要登录 CodeBuddy。\n\n登录页面将在 2 秒后打开，请在浏览器中完成登录。", messageBoxIconWarning)
		if err := runHidden(exe, []string{"auth", "login", "--config", configPath}, root); err != nil {
			timedMessage("CodeBuddy2API", "登录未完成。\n\n"+err.Error(), messageBoxIconError)
			return true
		}
		justLoggedIn = true
		timedMessage("CodeBuddy2API", "CodeBuddy 登录成功。\n\n提示将在 2 秒后关闭，网关随后转入后台运行。", messageBoxIconInformation)
	}

	if err := startServer(exe, root, configPath); err != nil {
		timedMessage("CodeBuddy2API", "服务启动失败：\n"+err.Error(), messageBoxIconError)
		return true
	}
	if !justLoggedIn {
		timedMessage("CodeBuddy2API", "启动成功，服务已在后台运行。\n\n控制台：http://127.0.0.1:8088/\n\n2 秒后自动关闭。", messageBoxIconInformation)
	}
	return true
}

func hideConsoleWindow() {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	user32 := windows.NewLazySystemDLL("user32.dll")
	getConsoleWindow := kernel32.NewProc("GetConsoleWindow")
	showWindow := user32.NewProc("ShowWindow")
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd != 0 {
		_, _, _ = showWindow.Call(hwnd, showWindowHide)
	}
}

func timedMessage(title, text string, icon uintptr) {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	textPtr, _ := syscall.UTF16PtrFromString(text)
	user32 := windows.NewLazySystemDLL("user32.dll")
	if proc := user32.NewProc("MessageBoxTimeoutW"); proc.Find() == nil {
		_, _, _ = proc.Call(0, uintptr(unsafe.Pointer(textPtr)), uintptr(unsafe.Pointer(titlePtr)), messageBoxOK|icon, 0, messageBoxTimeoutMs)
		return
	}
	messageBox := user32.NewProc("MessageBoxW")
	_, _, _ = messageBox.Call(0, uintptr(unsafe.Pointer(textPtr)), uintptr(unsafe.Pointer(titlePtr)), messageBoxOK|icon)
}

func runHidden(exe string, args []string, root string) error {
	logFile, err := openDesktopLog(root, "desktop-login.log")
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, args...)
	cmd.Dir = root
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd.Run()
}

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
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
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

func isGatewayRunning(root, exe string) bool {
	pidPath := filepath.Join(root, "codebuddy-gateway.pid")
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return false
	}
	var pid uint32
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err != nil || pid == 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var buffer [windows.MAX_PATH]uint16
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(h, 0, &buffer[0], &size); err != nil {
		return false
	}
	path := filepath.Clean(windows.UTF16ToString(buffer[:size]))
	return strings.EqualFold(path, filepath.Clean(exe))
}
