//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// pickScript 调 Windows 自带的文件夹选择对话框。
// 结果不走 stdout（PowerShell 默认按系统代码页输出，中文路径会乱码），
// 而是写成 UTF-8 文件再读回来，彻底绕开编码问题。
const pickScript = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms | Out-Null

# 这个脚本是隐藏窗口启动的，进程没有前台窗口，直接 ShowDialog() 弹出来的框
# 会被系统当成"无主窗口"，压在浏览器后面，用户得先回到桌面才看得见。
# 办法是造一个不可见、但 TopMost 的 Form 当 owner——对话框跟着它一起浮到最前。
$owner = New-Object System.Windows.Forms.Form
$owner.Text = 'LanSync'
$owner.FormBorderStyle = 'None'
$owner.ShowInTaskbar = $false
$owner.StartPosition = 'CenterScreen'
$owner.Size = New-Object System.Drawing.Size(1, 1)
$owner.Opacity = 0
$owner.TopMost = $true
$owner.Show()
$owner.Activate() | Out-Null

$dlg = New-Object System.Windows.Forms.FolderBrowserDialog
$dlg.Description = '选择手机资料的存放位置'
$dlg.ShowNewFolderButton = $true
if (CURDIR -ne '') { $dlg.SelectedPath = CURDIR }

$r = $dlg.ShowDialog($owner)
$owner.Close()
if ($r -eq [System.Windows.Forms.DialogResult]::OK) {
  [IO.File]::WriteAllText(OUTFILE, $dlg.SelectedPath, (New-Object System.Text.UTF8Encoding($false)))
  exit 0
}
exit 2
`

// psQuote 把路径包成 PowerShell 单引号字符串（内部单引号需双写）。
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// pickFolder 弹出系统「选择文件夹」对话框，返回用户选中的绝对路径。
// 用户点取消时返回空串，不算错误。
func pickFolder(current string) (string, error) {
	shell, err := exec.LookPath("powershell")
	if err != nil {
		if shell, err = exec.LookPath("pwsh"); err != nil {
			return "", errors.New("找不到 PowerShell，无法弹出选择框")
		}
	}

	outFile, err := os.CreateTemp("", "lansync-dir-*.txt")
	if err != nil {
		return "", err
	}
	outPath := outFile.Name()
	outFile.Close()
	os.Remove(outPath)
	defer os.Remove(outPath)

	script := strings.ReplaceAll(pickScript, "CURDIR", psQuote(current))
	script = strings.ReplaceAll(script, "OUTFILE", psQuote(outPath))

	// PowerShell 5.1 读无 BOM 的 UTF-8 会当成 ANSI，中文注释会乱码，所以带 BOM 写。
	psFile, err := os.CreateTemp("", "lansync-pick-*.ps1")
	if err != nil {
		return "", err
	}
	psPath := psFile.Name()
	defer os.Remove(psPath)
	_, _ = psFile.WriteString("\ufeff" + script)
	psFile.Close()

	cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-STA",
		"-ExecutionPolicy", "Bypass", "-File", psPath)
	// GUI 版没有控制台，不隐藏会闪一个黑窗口出来
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	runErr := cmd.Run()

	b, err := os.ReadFile(outPath)
	if err != nil {
		// 脚本退出码 2 表示用户点了取消，其它情况算失败（例如系统缺少 .NET 窗体组件）
		if ee, ok := runErr.(*exec.ExitError); ok && ee.ExitCode() == 2 {
			return "", nil
		}
		return "", errors.New("无法打开选择框，请手动输入路径")
	}
	return strings.TrimSpace(string(b)), nil
}

// fileScript 多选文件对话框。和目录选择框同一套套路：结果写 UTF-8 文件，
// 一行一个绝对路径，避开 PowerShell 代码页乱码。
const fileScript = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms | Out-Null

$owner = New-Object System.Windows.Forms.Form
$owner.Text = 'LanSync'
$owner.FormBorderStyle = 'None'
$owner.ShowInTaskbar = $false
$owner.StartPosition = 'CenterScreen'
$owner.Size = New-Object System.Drawing.Size(1, 1)
$owner.Opacity = 0
$owner.TopMost = $true
$owner.Show()
$owner.Activate() | Out-Null

$dlg = New-Object System.Windows.Forms.OpenFileDialog
$dlg.Title = '选择要发给手机的文件（可按住 Ctrl 多选）'
$dlg.Multiselect = $true
$dlg.CheckFileExists = $true
$dlg.RestoreDirectory = $true

$r = $dlg.ShowDialog($owner)
$owner.Close()
if ($r -eq [System.Windows.Forms.DialogResult]::OK) {
  [IO.File]::WriteAllLines(OUTFILE, $dlg.FileNames, (New-Object System.Text.UTF8Encoding($false)))
  exit 0
}
exit 2
`

// pickFiles 弹出系统「打开文件」对话框（支持多选），返回用户选中的绝对路径列表。
// 用户点取消返回空列表，不算错误。
func pickFiles() ([]string, error) {
	shell, err := exec.LookPath("powershell")
	if err != nil {
		if shell, err = exec.LookPath("pwsh"); err != nil {
			return nil, errors.New("找不到 PowerShell，无法弹出选择框")
		}
	}

	outFile, err := os.CreateTemp("", "lansync-files-*.txt")
	if err != nil {
		return nil, err
	}
	outPath := outFile.Name()
	outFile.Close()
	os.Remove(outPath)
	defer os.Remove(outPath)

	script := strings.ReplaceAll(fileScript, "OUTFILE", psQuote(outPath))

	psFile, err := os.CreateTemp("", "lansync-pickfile-*.ps1")
	if err != nil {
		return nil, err
	}
	psPath := psFile.Name()
	defer os.Remove(psPath)
	_, _ = psFile.WriteString("\ufeff" + script)
	psFile.Close()

	cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-STA",
		"-ExecutionPolicy", "Bypass", "-File", psPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	runErr := cmd.Run()

	b, err := os.ReadFile(outPath)
	if err != nil {
		if ee, ok := runErr.(*exec.ExitError); ok && ee.ExitCode() == 2 {
			return nil, nil // 用户点了取消
		}
		return nil, errors.New("无法打开选择框，可以直接把文件拖进页面")
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if v := strings.TrimSpace(line); v != "" {
			out = append(out, v)
		}
	}
	return out, nil
}
