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
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

const controllerUpdateBackupRetain = 7

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
	status, statusErr := s.controllerUpdater.Status(ctx)
	if statusErr == nil && (status.State == "failed" || status.State == "cancelled") {
		run.Phase = status.State
		run.Error = strings.TrimSpace(status.LastError)
		_ = s.store.UpdateControllerUpdateRun(ctx, run)
		s.publishRealtime("controller_update")
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

func (s *Server) preflightControllerUpdate(ctx context.Context, run *store.ControllerUpdateRun, backupDir string) error {
	if run == nil {
		return errors.New("update run is required")
	}
	pageCount, pageSize, _, err := s.store.DatabasePageStats(ctx)
	if err != nil {
		return fmt.Errorf("数据库不可读: %w", err)
	}
	dbSize := pageCount * pageSize
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return fmt.Errorf("备份目录不可写: %w", err)
	}
	probe := filepath.Join(backupDir, ".oboard-backup-writable")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return fmt.Errorf("备份目录不可写: %w", err)
	}
	_ = os.Remove(probe)
	free, err := store.DiskFreeBytes(backupDir)
	if err != nil {
		return fmt.Errorf("无法检查磁盘空间: %w", err)
	}
	need := store.RequiredBackupFreeBytes(dbSize)
	if free < need {
		return fmt.Errorf("磁盘空间不足：需要至少 %d 字节，当前可用 %d 字节", need, free)
	}
	if strings.TrimSpace(run.TargetBuild) == "" {
		return errors.New("目标构建尚未确定")
	}
	run.BackupSizeBytes = dbSize
	return nil
}

func (s *Server) persistControllerUpdateProgress(run *store.ControllerUpdateRun, progress store.BackupProgress) {
	if run == nil || run.ID <= 0 {
		return
	}
	s.controllerUpdateProgress.Store(progress)
	now := time.Now().UnixMilli()
	prev := s.controllerUpdateProgressAt.Load()
	if prev != 0 && now-prev < 1000 {
		return
	}
	s.controllerUpdateProgressAt.Store(now)
	run.BackupTotalPages = progress.TotalPages
	run.BackupRemainingPages = progress.RemainingPages
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.store.UpdateControllerUpdateRun(ctx, run)
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
