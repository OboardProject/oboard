package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/controllerupdate"
	"github.com/OboardProject/oboard/internal/storage"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

const controllerUpdateBackupRetain = 2
const controllerUpdateBackupRetainDefault = 2
const controllerUpdateStartupStaleReason = "主控更新未完成，重启时已自动清理；请重新尝试更新"
const controllerUpdateStaleTimeout = controllerUpdateInstallTimeout + 10*time.Minute
const controllerUpdateStartupGracePeriod = 2 * time.Minute

func controllerUpdateRunIsStale(run *store.ControllerUpdateRun, now time.Time) bool {
	if run == nil {
		return false
	}
	if !run.UpdatedAt.IsZero() {
		return now.Sub(run.UpdatedAt) > controllerUpdateStaleTimeout
	}
	if !run.StartedAt.IsZero() {
		return now.Sub(run.StartedAt) > controllerUpdateStaleTimeout
	}
	return true
}

func (s *Server) clearStaleControllerUpdateRunOnStartup(ctx context.Context, run *store.ControllerUpdateRun) {
	s.cancelControllerUpdateContext()
	s.cancelPreparedControllerUpdate()
	s.clearControllerUpdateMaintenance(ctx)
	_ = s.store.SetSetting(ctx, controllerUpdateErrorSetting, "")
	run.Phase = store.ControllerUpdatePhaseFailed
	run.Error = controllerUpdateStartupStaleReason
	if err := s.store.UpdateControllerUpdateRun(ctx, run); err != nil {
		log.Printf("controller update startup cleanup failed: %v", err)
		return
	}
	s.publishRealtime("controller_update")
	s.retainControllerUpdateBackups()
	log.Printf("controller update stalled run cleared on startup id=%d phase=%s target_build=%s", run.ID, run.Phase, run.TargetBuild)
}

func (s *Server) isControllerUpdateRunStaleOnStartup(run *store.ControllerUpdateRun, now time.Time, status controllerupdate.Status, statusErr error) bool {
	if run == nil {
		return false
	}
	// Any active phase outside installing/restarting cannot survive a restart:
	// the background goroutine that drives preflight/backup/download is gone.
	if run.Phase != store.ControllerUpdatePhaseInstalling && run.Phase != store.ControllerUpdatePhaseRestarting {
		return true
	}
	if controllerUpdateRunIsStale(run, now) {
		return true
	}
	if statusErr == nil && !isActiveControllerUpdateStatus(status.State) && status.State != "installing" {
		if now.Sub(run.UpdatedAt) > controllerUpdateStartupGracePeriod {
			return true
		}
	}
	return false
}

func (s *Server) recoverControllerUpdateRun(ctx context.Context) {
	run, err := s.store.GetActiveControllerUpdateRun(ctx)
	if err != nil || run == nil {
		s.retainControllerUpdateBackups()
		return
	}
	log.Printf("controller update recovery id=%d phase=%s target_build=%s current_build=%s", run.ID, run.Phase, run.TargetBuild, version.Build)
	if strings.TrimSpace(run.TargetBuild) != "" && strings.TrimSpace(run.TargetBuild) == strings.TrimSpace(version.Build) {
		run.Phase = store.ControllerUpdatePhaseVerifying
		if err := s.store.UpdateControllerUpdateRun(ctx, run); err != nil {
			log.Printf("controller update recovery verify: %v", err)
		}
		run.Phase = store.ControllerUpdatePhaseSucceeded
		run.Error = ""
		if err := s.store.UpdateControllerUpdateRun(ctx, run); err != nil {
			log.Printf("controller update recovery succeed: %v", err)
		}
		s.retainControllerUpdateBackups()
		s.publishRealtime("controller_update")
		log.Printf("controller update succeeded id=%d target_build=%s agents_independent=true", run.ID, run.TargetBuild)
		return
	}
	statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	status, statusErr := s.controllerUpdater.Status(statusCtx)
	cancel()
	if statusErr == nil && (status.State == "failed" || status.State == "cancelled") {
		run.Phase = status.State
		run.Error = strings.TrimSpace(status.LastError)
		_ = s.store.UpdateControllerUpdateRun(ctx, run)
		s.publishRealtime("controller_update")
		return
	}
	if s.isControllerUpdateRunStaleOnStartup(run, time.Now().UTC(), status, statusErr) {
		s.clearStaleControllerUpdateRunOnStartup(ctx, run)
		return
	}
	if run.Phase == store.ControllerUpdatePhaseInstalling || run.Phase == store.ControllerUpdatePhaseRestarting {
		s.startControllerUpdateWatch()
	}
}

func (s *Server) attachControllerUpdateOperation(ctx context.Context, status *controllerupdate.Status) {
	if status == nil {
		return
	}
	run, err := s.store.GetActiveControllerUpdateRun(ctx)
	if err != nil {
		return
	}
	if run == nil {
		latest, latestErr := s.store.LatestControllerUpdateRun(ctx)
		if latestErr != nil || latest == nil {
			return
		}
		if latest.Phase == store.ControllerUpdatePhaseSucceeded || latest.Phase == store.ControllerUpdatePhaseFailed || latest.Phase == store.ControllerUpdatePhaseCancelled {
			status.Operation = controllerUpdateOperationFromRun(latest, nil, false)
			if latest.Phase == store.ControllerUpdatePhaseCancelled && latest.Error == controllerUpdateForceFinishedReason && isActiveControllerUpdateStatus(status.State) {
				status.State = store.ControllerUpdatePhaseCancelled
				status.CanCancel = false
				status.LastError = ""
			}
		}
		return
	}
	progress, _ := s.controllerUpdateProgress.Load().(store.BackupProgress)
	status.Operation = controllerUpdateOperationFromRun(run, &progress, true)
	switch run.Phase {
	case store.ControllerUpdatePhasePreflight, store.ControllerUpdatePhaseBackingUp, store.ControllerUpdatePhaseRestarting, store.ControllerUpdatePhaseVerifying:
		status.State = run.Phase
	}
	if controllerUpdatePhaseCancellable(run.Phase) {
		status.CanCancel = true
	}
}

func controllerUpdateOperationFromRun(run *store.ControllerUpdateRun, progress *store.BackupProgress, active bool) *controllerupdate.UpdateOperation {
	if run == nil {
		return nil
	}
	op := &controllerupdate.UpdateOperation{
		Active:      active,
		Phase:       run.Phase,
		TargetBuild: run.TargetBuild,
	}
	if !run.StartedAt.IsZero() {
		op.StartedAt = run.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if progress != nil && progress.TotalPages > 0 {
		op.ProgressPercent = progress.Percent
		op.Backup = &controllerupdate.UpdateBackupProgress{TotalPages: progress.TotalPages, RemainingPages: progress.RemainingPages, SizeBytes: run.BackupSizeBytes}
	} else if run.BackupTotalPages > 0 {
		completed := run.BackupTotalPages - run.BackupRemainingPages
		if run.BackupTotalPages > 0 {
			op.ProgressPercent = float64(completed) * 100 / float64(run.BackupTotalPages)
		}
		op.Backup = &controllerupdate.UpdateBackupProgress{TotalPages: run.BackupTotalPages, RemainingPages: run.BackupRemainingPages, SizeBytes: run.BackupSizeBytes}
	}
	return op
}

func controllerUpdatePhaseCancellable(phase string) bool {
	switch strings.TrimSpace(phase) {
	case store.ControllerUpdatePhaseChecking, store.ControllerUpdatePhaseDownloading, store.ControllerUpdatePhasePreflight, store.ControllerUpdatePhaseBackingUp:
		return true
	default:
		return false
	}
}

func (s *Server) setControllerUpdateCancel(cancel context.CancelFunc) {
	s.controllerUpdateCancelMu.Lock()
	defer s.controllerUpdateCancelMu.Unlock()
	if s.controllerUpdateAbort != nil {
		s.controllerUpdateAbort()
	}
	s.controllerUpdateAbort = cancel
}

func (s *Server) cancelControllerUpdateContext() {
	s.controllerUpdateCancelMu.Lock()
	cancel := s.controllerUpdateAbort
	s.controllerUpdateAbort = nil
	s.controllerUpdateCancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) preflightControllerUpdate(ctx context.Context, run *store.ControllerUpdateRun, backupDir string, skipBackup bool) error {
	if run == nil {
		return errors.New("update run is required")
	}
	if strings.TrimSpace(run.TargetBuild) == "" {
		return errors.New("目标构建尚未确定")
	}
	if skipBackup {
		return nil
	}
	if err := storage.EnsureDirectoryWritable(backupDir); err != nil {
		return fmt.Errorf("备份目录不可写: %w", err)
	}
	if err := s.checkControllerDiskBudget(ctx, backupDir, skipBackup); err != nil {
		return err
	}
	pageCount, pageSize, _, err := s.store.DatabasePageStats(ctx)
	if err != nil {
		return fmt.Errorf("数据库不可读: %w", err)
	}
	dbSize := pageCount * pageSize
	run.BackupSizeBytes = dbSize
	return nil
}

func (s *Server) checkControllerDiskBudget(ctx context.Context, backupDir string, skipBackup bool) error {
	// Check backupDir filesystem
	stats, err := storage.StatFilesystem(backupDir)
	if err != nil {
		return fmt.Errorf("无法检查磁盘空间: %w", err)
	}
	reserve := storage.ControllerReserve(stats.TotalBytes)
	var required uint64
	if !skipBackup {
		pageCount, pageSize, _, err := s.store.DatabasePageStats(ctx)
		if err == nil {
			dbSize := uint64(pageCount * pageSize)
			need := uint64(store.RequiredBackupFreeBytes(int64(dbSize)))
			// RequiredBackupFreeBytes already includes reserve-like headroom, but we also enforce reserve
			required = need
			// If need already > reserve, reserve is double counted, take max
			if reserve > 0 && need < dbSize+reserve {
				required = dbSize + reserve
			}
		}
	} else {
		// Even without backup, ensure we have reserve headroom
		required = reserve / 2
		if required == 0 {
			required = reserve
		}
	}
	if stats.AvailableBytes < required+reserve/2 {
		// Attempt light GC: clean stale temp files and excess backups before failing
		s.cleanupControllerDiskPressure(backupDir)
		stats2, err2 := storage.StatFilesystem(backupDir)
		if err2 == nil {
			stats = stats2
		}
		if stats.AvailableBytes < required {
			return fmt.Errorf("磁盘空间不足：需要至少 %d 字节，当前可用 %d 字节", required, stats.AvailableBytes)
		}
	}
	// Also check download/work dir if different filesystem
	if workDir := s.controllerBackupDir; workDir != "" && workDir != backupDir {
		if _, err := storage.StatFilesystem(workDir); err == nil {
			// best effort, already covered
		}
	}
	return nil
}

func (s *Server) cleanupControllerDiskPressure(backupDir string) {
	// Order: stale tmp, excess update snapshots, expired pre_restore, excess automatic backups
	_ = os.Remove(filepath.Join(backupDir, ".oboard-backup-writable"))
	s.cleanupZeroByteControllerUpdateBackupFiles()
	s.retainControllerUpdateBackups()
	_ = s.pruneExpiredPreRestoreBackups(context.Background())
	if s.backupManager != nil {
		_, _ = s.backupManager.RemoveZeroByteBackups()
	}
}

func (s *Server) controllerUpdateRetention() int {
	settings, err := s.store.ListSettings(context.Background())
	if err != nil {
		return controllerUpdateBackupRetainDefault
	}
	val := strings.TrimSpace(settings[controllerUpdateBackupRetentionSetting])
	if val == "" {
		// fallback to backup setting key
		val = strings.TrimSpace(settings[controllerBackupUpdateRetentionSetting])
	}
	if val == "" {
		return controllerUpdateBackupRetainDefault
	}
	if n, err := parseRetentionValue(val, 0, 10); err == nil {
		return n
	}
	return controllerUpdateBackupRetainDefault
}

func parseRetentionValue(raw string, min, max int) (int, error) {
	v, err := parseIntString(raw)
	if err != nil {
		return 0, err
	}
	if v < min || v > max {
		return 0, errors.New("out of range")
	}
	return v, nil
}

func parseIntString(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("empty")
	}
	var n int
	_, err := fmt.Sscanf(raw, "%d", &n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Server) failControllerUpdateRun(ctx context.Context, run *store.ControllerUpdateRun, message string) {
	if run == nil {
		return
	}
	run.Phase = store.ControllerUpdatePhaseFailed
	run.Error = strings.TrimSpace(message)
	_ = s.store.UpdateControllerUpdateRun(ctx, run)
	s.publishRealtime("controller_update")
}

func (s *Server) cancelControllerUpdateRun(ctx context.Context, run *store.ControllerUpdateRun) {
	if run == nil {
		return
	}
	run.Phase = store.ControllerUpdatePhaseCancelled
	run.Error = ""
	_ = s.store.UpdateControllerUpdateRun(ctx, run)
	s.publishRealtime("controller_update")
}
