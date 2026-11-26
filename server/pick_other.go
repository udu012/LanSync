//go:build !windows

package main

import "errors"

// pickFolder 在非 Windows 平台没有统一可用的系统目录选择框，
// 前端会退回到「手动输入路径」的方式。
func pickFolder(current string) (string, error) {
	return "", errors.New("当前系统不支持弹出选择框")
}
