Option Explicit

Dim shell, fso, root, exe
Set shell = CreateObject("WScript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")

root = fso.GetParentFolderName(WScript.ScriptFullName)
exe = fso.BuildPath(root, "codebuddy-gateway.exe")

If Not fso.FileExists(exe) Then
    shell.Popup "找不到 codebuddy-gateway.exe。请将此脚本放在程序目录中。", 5, "CodeBuddy2API", 16
    WScript.Quit 1
End If

shell.CurrentDirectory = root
' 无窗口、异步启动。首次运行会自动打开登录页；已有登录态则直接后台运行。
shell.Run Chr(34) & exe & Chr(34), 0, False
