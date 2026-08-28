package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"modernc.org/sqlite"
)

const (
	defaultBackupPagesPerStep     int32 = 1024
	backupProgressMinInterval           = 250 * time.Millisecond
	backupProgressMinPercentDelta       = 1.0
)

var backupBusyBackoff = []time.Duration{
	10 * time.Millisecond,
	25 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
}

// BackupProgress reports Online Backup page copy state.
type BackupProgress struct {
	TotalPages     int
	RemainingPages int
	CompletedPages int
	Percent        float64
	StartedAt      time.Time
	Elapsed        time.Duration
}

// BackupOptions controls SQLite Online Backup batching and progress.
type BackupOptions struct {
	PagesPerStep int32
	Progress     func(BackupProgress)
}

type sqliteBackuper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

// Backup writes a transactionally consistent standalone SQLite snapshot using
// the Online Backup API. It copies pages in batches and never VACUUMs.
func (s *Store) Backup(ctx context.Context, destination string, options BackupOptions) error {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return errors.New("backup destination is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return err
	}
	pagesPerStep := options.PagesPerStep
	if pagesPerStep <= 0 {
		pagesPerStep = defaultBackupPagesPerStep
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve SQLite backup connection: %w", err)
	}
	defer conn.Close()
	var backupErr error
	rawErr := conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(sqliteBackuper)
		if !ok {
			return fmt.Errorf("SQLite driver does not support online backup")
		}
		backupErr = runOnlineBackup(ctx, backuper, destination, pagesPerStep, options.Progress)
		return nil
	})
	if rawErr != nil {
		_ = os.Remove(destination)
		return rawErr
	}
	if backupErr != nil {
		_ = os.Remove(destination)
		return backupErr
	}
	return nil
}

func runOnlineBackup(ctx context.Context, backuper sqliteBackuper, destination string, pagesPerStep int32, progress func(BackupProgress)) error {
	bck, err := backuper.NewBackup(destination)
	if err != nil {
		return fmt.Errorf("create SQLite backup: %w", err)
	}
	committed := false
	var destConn driver.Conn
	defer func() {
		if !committed {
			_ = bck.Finish()
			_ = os.Remove(destination)
			return
		}
		if destConn != nil {
			_ = destConn.Close()
		}
	}()
	startedAt := time.Now()
	lastProgressAt := time.Time{}
	lastPercent := -1.0
	busyAttempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		more, stepErr := bck.Step(pagesPerStep)
		if stepErr != nil {
			if IsSQLiteBackupRetryable(stepErr) {
				delay := backupBusyBackoff[busyAttempt]
				if busyAttempt+1 < len(backupBusyBackoff) {
					busyAttempt++
				}
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				continue
			}
			return fmt.Errorf("copy SQLite backup pages: %w", stepErr)
		}
		busyAttempt = 0
		total := bck.PageCount()
		remaining := bck.Remaining()
		completed := total - remaining
		if completed < 0 {
			completed = 0
		}
		percent := 0.0
		if total > 0 {
			percent = (float64(completed) / float64(total)) * 100
		}
		now := time.Now()
		shouldReport := lastProgressAt.IsZero() || now.Sub(lastProgressAt) >= backupProgressMinInterval || percent-lastPercent >= backupProgressMinPercentDelta || !more
		if shouldReport && progress != nil {
			progress(BackupProgress{
				TotalPages:     total,
				RemainingPages: remaining,
				CompletedPages: completed,
				Percent:        percent,
				StartedAt:      startedAt,
				Elapsed:        now.Sub(startedAt),
			})
			lastProgressAt = now
			lastPercent = percent
		}
		if !more {
			break
		}
	}
	destConn, err = bck.Commit()
	if err != nil {
		return fmt.Errorf("commit SQLite backup: %w", err)
	}
	committed = true
	if destConn != nil {
		if closeErr := destConn.Close(); closeErr != nil {
			_ = os.Remove(destination)
			return fmt.Errorf("close SQLite backup destination: %w", closeErr)
		}
		destConn = nil
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("secure SQLite backup: %w", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("stat SQLite backup: %w", err)
	}
	if info.Size() <= 0 {
		_ = os.Remove(destination)
		return errors.New("SQLite backup is empty")
	}
	if err := fsyncPath(destination); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("fsync SQLite backup: %w", err)
	}
	return nil
}

func fsyncPath(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// DatabasePageStats returns SQLite page_count, page_size, and freelist_count.
func (s *Store) DatabasePageStats(ctx context.Context) (pageCount, pageSize, freelistCount int64, err error) {
	if err = s.db.QueryRowContext(ctx, `pragma page_count`).Scan(&pageCount); err != nil {
		return 0, 0, 0, err
	}
	if err = s.db.QueryRowContext(ctx, `pragma page_size`).Scan(&pageSize); err != nil {
		return 0, 0, 0, err
	}
	if err = s.db.QueryRowContext(ctx, `pragma freelist_count`).Scan(&freelistCount); err != nil {
		return 0, 0, 0, err
	}
	return pageCount, pageSize, freelistCount, nil
}

// DatabaseFragmentation returns freelist_count/page_count.
func (s *Store) DatabaseFragmentation(ctx context.Context) (pageCount, freelistCount int64, ratio float64, err error) {
	pageCount, _, freelistCount, err = s.DatabasePageStats(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	if pageCount > 0 {
		ratio = float64(freelistCount) / float64(pageCount)
	}
	return pageCount, freelistCount, ratio, nil
}

// RequiredBackupFreeBytes is the disk floor that keeps an update backup from filling the volume.
func RequiredBackupFreeBytes(dbSize int64) int64 {
	if dbSize < 0 {
		dbSize = 0
	}
	padded := dbSize + dbSize/4
	withHeadroom := dbSize + 512<<20
	if padded > withHeadroom {
		return padded
	}
	return withHeadroom
}

// DiskFreeBytes reports available bytes for new files on the volume that contains path.
func DiskFreeBytes(path string) (int64, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, errors.New("disk path is required")
	}
	info, err := os.Stat(path)
	check := path
	if err != nil {
		if !os.IsNotExist(err) {
			return 0, err
		}
		check = filepath.Dir(path)
	} else if !info.IsDir() {
		check = filepath.Dir(path)
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(check, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

// IsSQLiteLocked reports SQLITE_LOCKED and extended locked result codes.
func IsSQLiteLocked(err error) bool {
	var coded interface{ Code() int }
	if !errors.As(err, &coded) {
		return false
	}
	return coded.Code()&0xff == 6
}

// IsSQLiteConstraint reports SQLITE_CONSTRAINT and extended constraint codes.
func IsSQLiteConstraint(err error) bool {
	var coded interface{ Code() int }
	if !errors.As(err, &coded) {
		return false
	}
	return coded.Code()&0xff == 19
}

// IsSQLiteBackupRetryable reports transient backup-step conflicts.
func IsSQLiteBackupRetryable(err error) bool {
	return IsSQLiteBusy(err) || IsSQLiteLocked(err)
}
