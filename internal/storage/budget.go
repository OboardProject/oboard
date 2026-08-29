package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func EnsureDirectoryWritable(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".oboard-writable-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return err
	}
	_ = os.Remove(probe)
	return nil
}

func RemoveStaleTempFiles(dir string, ttlCheck func(os.FileInfo) bool) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".tmp") && !strings.HasPrefix(name, "stage-") && !strings.HasPrefix(name, ".oboard-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if ttlCheck != nil && !ttlCheck(info) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err == nil {
			removed++
		}
	}
	return removed
}
