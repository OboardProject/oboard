package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type FilesystemStats struct {
	TotalBytes     uint64 `json:"total_bytes"`
	FreeBytes      uint64 `json:"free_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	UsagePercent   float64 `json:"usage_percent"`
}

type DiskRequirement struct {
	RequiredBytes uint64 `json:"required_bytes"`
	ReserveBytes  uint64 `json:"reserve_bytes"`
}

func StatFilesystem(path string) (FilesystemStats, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return FilesystemStats{}, errors.New("path is required")
	}
	info, err := os.Stat(path)
	check := path
	if err != nil {
		if !os.IsNotExist(err) {
			return FilesystemStats{}, err
		}
		check = filepath.Dir(path)
	} else if !info.IsDir() {
		check = filepath.Dir(path)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(check, &st); err != nil {
		return FilesystemStats{}, err
	}
	bsize := int64(st.Bsize)
	if bsize <= 0 {
		return FilesystemStats{}, errors.New("invalid block size")
	}
	total := uint64(st.Blocks) * uint64(bsize)
	free := uint64(st.Bfree) * uint64(bsize)
	avail := uint64(st.Bavail) * uint64(bsize)
	var used uint64
	if st.Blocks > st.Bfree {
		used = (st.Blocks - st.Bfree) * uint64(bsize)
	}
	var pct float64
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	return FilesystemStats{
		TotalBytes:     total,
		FreeBytes:      free,
		AvailableBytes: avail,
		UsedBytes:      used,
		UsagePercent:   pct,
	}, nil
}

func CheckRequirement(path string, req DiskRequirement) (FilesystemStats, error) {
	stats, err := StatFilesystem(path)
	if err != nil {
		return FilesystemStats{}, err
	}
	needed := req.RequiredBytes + req.ReserveBytes
	if stats.AvailableBytes < needed {
		return stats, &InsufficientSpaceError{
			Path:      path,
			Required:  req.RequiredBytes,
			Reserve:   req.ReserveBytes,
			Available: stats.AvailableBytes,
			Total:     stats.TotalBytes,
		}
	}
	return stats, nil
}

type InsufficientSpaceError struct {
	Path      string
	Required  uint64
	Reserve   uint64
	Available uint64
	Total     uint64
}

func (e *InsufficientSpaceError) Error() string {
	return "insufficient disk space"
}

func ControllerReserve(total uint64) uint64 {
	tenPercent := total / 10
	reserve := uint64(256 << 20)
	if tenPercent > reserve {
		reserve = tenPercent
	}
	return reserve
}

func AgentReserve(total uint64, profile string) uint64 {
	var base uint64
	switch profile {
	case "tiny":
		base = 32 << 20
	case "small":
		base = 64 << 20
	default:
		base = 128 << 20
	}
	fivePercent := total / 20
	if fivePercent > base {
		base = fivePercent
	}
	return base
}
