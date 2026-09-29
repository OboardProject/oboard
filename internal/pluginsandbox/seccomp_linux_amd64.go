//go:build linux && amd64

package pluginsandbox

import "golang.org/x/sys/unix"

const auditArch = 0xc000003e // AUDIT_ARCH_X86_64

func archDeniedSyscalls() []uintptr {
	return []uintptr{unix.SYS_FORK, unix.SYS_VFORK, unix.SYS_ACCEPT, unix.SYS_IOPL, unix.SYS_IOPERM, unix.SYS_MKNOD}
}
