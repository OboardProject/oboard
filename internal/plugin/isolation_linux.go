package plugin

import (
	"os"
	"os/exec"
	"syscall"
)

func attachCgroup(cmd *exec.Cmd, group *os.File) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(group.Fd())}
	return nil
}
