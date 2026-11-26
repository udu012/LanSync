//go:build !windows

package main

import "errors"

// pickFolder 在非 Windows 平台没有统一可用的系统目录选择框，
// 前端会退回到「手动输入路径」的方式。
func pickFolder(current string) (string, error) {
	return "", errors.New("当前系统不支持弹出选择框")
}

// pickFiles 在非 Windows 平台没有统一的多选文件框，看板会退回到「拖拽」方式。
func pickFiles() ([]string, error) {
	return nil, errors.New("当前系统不支持弹出选择框，请把文件拖进页面")
}
