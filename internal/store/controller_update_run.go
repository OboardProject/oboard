package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ControllerUpdatePhaseIdle        = "idle"
	ControllerUpdatePhaseChecking    = "checking"
	ControllerUpdatePhaseDownloading = "downloading"
	ControllerUpdatePhasePreflight   = "preflight"
	ControllerUpdatePhaseBackingUp   = "backing_up"
	ControllerUpdatePhaseInstalling  = "installing"
	ControllerUpdatePhaseRestarting  = "restarting"
	ControllerUpdatePhaseVerifying   = "verifying"
	ControllerUpdatePhaseSucceeded   = "succeeded"
	ControllerUpdatePhaseFailed      = "failed"
	ControllerUpdatePhaseCancelled   = "cancelled"
)

// ControllerUpdateRun is a persisted Controller software-update operation.
type ControllerUpdateRun struct {
	ID                   int64
	Source               string
	CurrentVersion       string
	CurrentBuild         string
	TargetVersion        string
	TargetBuild          string
	Phase                string
	StartedAt            time.Time
	UpdatedAt            time.Time
	FinishedAt           *time.Time
	BackupPath           string
	BackupTotalPages     int
	BackupRemainingPages int
	BackupSizeBytes      int64
	DownloadDurationMS   int64
	BackupDurationMS     int64
	InstallDurationMS    int64
	RestartDurationMS    int64
	TotalDurationMS      int64
	Error                string
}

func controllerUpdateRunActive(phase string) bool {
	switch strings.TrimSpace(phase) {
	case ControllerUpdatePhaseSucceeded, ControllerUpdatePhaseFailed, ControllerUpdatePhaseCancelled, ControllerUpdatePhaseIdle, "":
		return false
	default:
		return true
	}
}

func (s *Store) CreateControllerUpdateRun(ctx context.Context, run *ControllerUpdateRun) error {
	if run == nil {
		return errors.New("update run is required")
	}
	if strings.TrimSpace(run.Phase) == "" {
		run.Phase = ControllerUpdatePhaseChecking
	}
	if strings.TrimSpace(run.Source) == "" {
		run.Source = "manual"
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into controller_update_runs(source,current_version,current_build,target_version,target_build,phase,started_at,updated_at,backup_path,backup_total_pages,backup_remaining_pages,backup_size_bytes,error) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.Source, run.CurrentVersion, run.CurrentBuild, run.TargetVersion, run.TargetBuild, run.Phase, ts, ts, run.BackupPath, run.BackupTotalPages, run.BackupRemainingPages, run.BackupSizeBytes, run.Error)
	if err != nil {
		if IsSQLiteConstraint(err) {
			return errors.New("an active Controller update is already running")
		}
		return err
	}
	run.ID, _ = res.LastInsertId()
	run.StartedAt = parseTime(ts)
	run.UpdatedAt = run.StartedAt
	return nil
}

func (s *Store) GetControllerUpdateRun(ctx context.Context, id int64) (*ControllerUpdateRun, error) {
	return s.scanControllerUpdateRun(s.db.QueryRowContext(ctx, controllerUpdateRunSelect+` where id=?`, id))
}

func (s *Store) GetActiveControllerUpdateRun(ctx context.Context) (*ControllerUpdateRun, error) {
	run, err := s.scanControllerUpdateRun(s.db.QueryRowContext(ctx, controllerUpdateRunSelect+` where phase not in ('succeeded','failed','cancelled') order by id desc limit 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return run, err
}

func (s *Store) LatestControllerUpdateRun(ctx context.Context) (*ControllerUpdateRun, error) {
	run, err := s.scanControllerUpdateRun(s.db.QueryRowContext(ctx, controllerUpdateRunSelect+` order by id desc limit 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return run, err
}

func (s *Store) UpdateControllerUpdateRun(ctx context.Context, run *ControllerUpdateRun) error {
	if run == nil || run.ID <= 0 {
		return errors.New("update run id is required")
	}
	ts := now()
	var finished any
	if !controllerUpdateRunActive(run.Phase) {
		finished = ts
		t := parseTime(ts)
		run.FinishedAt = &t
		if run.TotalDurationMS == 0 && !run.StartedAt.IsZero() {
			run.TotalDurationMS = t.Sub(run.StartedAt).Milliseconds()
		}
	} else {
		finished = nilTime(run.FinishedAt)
	}
	active := controllerUpdateRunActive(run.Phase)
	result, err := s.db.ExecContext(ctx, `update controller_update_runs set source=?,current_version=?,current_build=?,target_version=?,target_build=?,phase=?,updated_at=?,finished_at=?,backup_path=?,backup_total_pages=?,backup_remaining_pages=?,backup_size_bytes=?,download_duration_ms=?,backup_duration_ms=?,install_duration_ms=?,restart_duration_ms=?,total_duration_ms=?,error=? where id=? and phase not in ('succeeded','failed','cancelled')`,
		run.Source, run.CurrentVersion, run.CurrentBuild, run.TargetVersion, run.TargetBuild, run.Phase, ts, finished, run.BackupPath, run.BackupTotalPages, run.BackupRemainingPages, run.BackupSizeBytes, run.DownloadDurationMS, run.BackupDurationMS, run.InstallDurationMS, run.RestartDurationMS, run.TotalDurationMS, run.Error, run.ID)
	if err != nil {
		return err
	}
	if active {
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
			return affectedErr
		} else if affected == 0 {
			return fmt.Errorf("Controller update run %d is already terminal", run.ID)
		}
	}
	run.UpdatedAt = parseTime(ts)
	return nil
}

func (s *Store) ForceFinishActiveControllerUpdateRun(ctx context.Context, reason string) (*ControllerUpdateRun, bool, error) {
	run, err := s.GetActiveControllerUpdateRun(ctx)
	if err != nil || run == nil {
		return run, false, err
	}
	run.Phase = ControllerUpdatePhaseCancelled
	run.Error = strings.TrimSpace(reason)
	if err := s.UpdateControllerUpdateRun(ctx, run); err != nil {
		return nil, false, err
	}
	return run, true, nil
}

const controllerUpdateRunSelect = `select id,source,current_version,current_build,target_version,target_build,phase,started_at,updated_at,finished_at,backup_path,backup_total_pages,backup_remaining_pages,backup_size_bytes,download_duration_ms,backup_duration_ms,install_duration_ms,restart_duration_ms,total_duration_ms,error from controller_update_runs`

func (s *Store) scanControllerUpdateRun(row *sql.Row) (*ControllerUpdateRun, error) {
	var run ControllerUpdateRun
	var started, updated string
	var finished sql.NullString
	if err := row.Scan(&run.ID, &run.Source, &run.CurrentVersion, &run.CurrentBuild, &run.TargetVersion, &run.TargetBuild, &run.Phase, &started, &updated, &finished, &run.BackupPath, &run.BackupTotalPages, &run.BackupRemainingPages, &run.BackupSizeBytes, &run.DownloadDurationMS, &run.BackupDurationMS, &run.InstallDurationMS, &run.RestartDurationMS, &run.TotalDurationMS, &run.Error); err != nil {
		return nil, err
	}
	run.StartedAt = parseTime(started)
	run.UpdatedAt = parseTime(updated)
	if finished.Valid && finished.String != "" {
		t := parseTime(finished.String)
		run.FinishedAt = &t
	}
	return &run, nil
}
