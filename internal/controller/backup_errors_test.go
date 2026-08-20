package controller

import (
	"errors"
	"testing"
)

func TestLocalizeBackupSystemErrors(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"create SQLite backup: no space left on device", "磁盘空间不足"},
		{"write backup: disk quota exceeded", "磁盘配额已用完"},
		{"open backup: read-only file system", "磁盘为只读状态，无法写入"},
		{"open backup: permission denied", "没有操作权限"},
		{"read backup: input/output error", "读取或写入失败"},
		{"already localized 磁盘空间不足", "already localized 磁盘空间不足"},
	}
	for _, test := range tests {
		if got := localizeBackupErrorMessage(test.input); got != test.want {
			t.Errorf("localizeBackupErrorMessage(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	if got := localizeBackupError(errors.New("create SQLite backup: no space left on device")).Error(); got != "磁盘空间不足" {
		t.Fatalf("localizeBackupError = %q", got)
	}
}
