package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestForceFinishControllerUpdateRunIsTerminalAndIdempotent(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	run := &ControllerUpdateRun{Source: "manual", TargetBuild: "20260828194806", Phase: ControllerUpdatePhaseVerifying}
	if err := db.CreateControllerUpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	finished, changed, err := db.ForceFinishActiveControllerUpdateRun(ctx, "管理员强制结束更新任务")
	if err != nil || !changed || finished == nil || finished.Phase != ControllerUpdatePhaseCancelled || finished.FinishedAt == nil {
		t.Fatalf("finished=%#v changed=%t err=%v", finished, changed, err)
	}
	if active, err := db.GetActiveControllerUpdateRun(ctx); err != nil || active != nil {
		t.Fatalf("active=%#v err=%v", active, err)
	}
	if _, changed, err := db.ForceFinishActiveControllerUpdateRun(ctx, "again"); err != nil || changed {
		t.Fatalf("second force finish changed=%t err=%v", changed, err)
	}

	run.Phase = ControllerUpdatePhaseRestarting
	if err := db.UpdateControllerUpdateRun(ctx, run); err == nil || !strings.Contains(err.Error(), "already terminal") {
		t.Fatalf("terminal run was reactivated: %v", err)
	}
	run.Phase = ControllerUpdatePhaseFailed
	run.Error = "late background failure"
	if err := db.UpdateControllerUpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetControllerUpdateRun(ctx, run.ID)
	if err != nil || stored.Phase != ControllerUpdatePhaseCancelled || stored.Error != "管理员强制结束更新任务" {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}
