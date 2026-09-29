//go:build linux && arm64

package pluginsandbox

import "golang.org/x/sys/unix"

const auditArch = 0xc00000b7 // AUDIT_ARCH_AARCH64

func archDeniedSyscalls() []uintptr {
	return []uintptr{unix.SYS_ACCEPT}
}
