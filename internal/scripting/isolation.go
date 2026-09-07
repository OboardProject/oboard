package scripting

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type IsolationStatus struct {
	Available bool
	Mode      string
	Reason    string
}

func ProbeIsolation() IsolationStatus {
	if runtime.GOOS != "linux" {
		return IsolationStatus{Mode: "unavailable", Reason: "script isolation requires Linux namespaces and is unavailable on " + runtime.GOOS}
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		return IsolationStatus{Mode: "unavailable", Reason: "bubblewrap (bwrap) is not installed"}
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return IsolationStatus{Mode: "unavailable", Reason: "cgroup v2 is not available"}
	}
	if strings.TrimSpace(os.Getenv("OBOARD_SCRIPT_DISABLE_ISOLATION")) == "1" {
		return IsolationStatus{Mode: "unavailable", Reason: "script isolation is disabled by administrator"}
	}
	return IsolationStatus{Available: true, Mode: "bubblewrap+cgroupv2"}
}
