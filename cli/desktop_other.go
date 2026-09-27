//go:build !windows && !darwin

package cli

// RunDesktop 仅在 Windows / macOS 双击场景生效；其它系统继续使用命令行入口。
func RunDesktop() bool { return false }
