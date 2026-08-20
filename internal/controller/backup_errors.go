package controller

import (
	"errors"
	"strings"
)

var backupSystemErrors = []struct {
	marker  string
	message string
}{
	{"no space left on device", "磁盘空间不足"},
	{"not enough space", "磁盘空间不足"},
	{"disk quota exceeded", "磁盘配额已用完"},
	{"read-only file system", "磁盘为只读状态，无法写入"},
	{"permission denied", "没有操作权限"},
	{"operation not permitted", "操作不被允许"},
	{"input/output error", "读取或写入失败"},
	{"too many open files", "打开的文件过多"},
	{"device or resource busy", "设备或资源正忙"},
}

func localizeBackupErrorMessage(message string) string {
	lower := strings.ToLower(message)
	for _, item := range backupSystemErrors {
		if strings.Contains(lower, item.marker) {
			return item.message
		}
	}
	return message
}

func localizeBackupError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(localizeBackupErrorMessage(err.Error()))
}
