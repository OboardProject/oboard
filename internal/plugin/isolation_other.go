//go:build !linux

package plugin

import (
	"fmt"
	"os"
	"os/exec"
)

func attachCgroup(_ *exec.Cmd, _ *os.File) error { return fmt.Errorf("cgroup v2 requires Linux") }
