//go:build linux && (amd64 || arm64)

package pluginsandbox

import (
	"fmt"
	"unsafe"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

const seccompFilterTSync = 1

// deniedSyscalls can never succeed inside a runner: no new programs, no
// processes, no sockets, no namespace or mount changes, no kernel keyrings,
// no module or BPF loading, no cross-process memory access.
func deniedSyscalls() []uint32 {
	names := []uintptr{
		unix.SYS_EXECVE, unix.SYS_EXECVEAT, unix.SYS_CLONE3,
		unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN, unix.SYS_ACCEPT4,
		unix.SYS_PTRACE, unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT,
		unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY, unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN,
		unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_USERFAULTFD, unix.SYS_KEXEC_LOAD, unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE,
		unix.SYS_DELETE_MODULE, unix.SYS_REBOOT, unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_PROCESS_VM_READV,
		unix.SYS_PROCESS_VM_WRITEV, unix.SYS_MKNODAT, unix.SYS_SYSLOG, unix.SYS_ACCT, unix.SYS_SETHOSTNAME,
		unix.SYS_SETDOMAINNAME, unix.SYS_MEMFD_CREATE,
	}
	names = append(names, archDeniedSyscalls()...)
	out := make([]uint32, 0, len(names))
	for _, nr := range names {
		out = append(out, uint32(nr))
	}
	return out
}

func seccompProgram() ([]bpf.RawInstruction, error) {
	return buildSeccompProgram(auditArch, deniedSyscalls(), uint32(unix.SYS_CLONE))
}

// HardenRunner is called first thing inside the runner process. It lowers
// resource limits and installs the seccomp filter on every thread; any
// failure aborts the run instead of continuing unconfined.
func HardenRunner() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	for resource, limit := range map[int]uint64{unix.RLIMIT_NOFILE: 32, unix.RLIMIT_CORE: 0, unix.RLIMIT_FSIZE: 1 << 20, unix.RLIMIT_NPROC: 64} {
		if err := unix.Setrlimit(resource, &unix.Rlimit{Cur: limit, Max: limit}); err != nil {
			return fmt.Errorf("setrlimit %d: %w", resource, err)
		}
	}
	instructions, err := seccompProgram()
	if err != nil {
		return err
	}
	filters := make([]unix.SockFilter, len(instructions))
	for i, instruction := range instructions {
		filters[i] = unix.SockFilter{Code: instruction.Op, Jt: instruction.Jt, Jf: instruction.Jf, K: instruction.K}
	}
	prog := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if _, _, errno := unix.RawSyscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, seccompFilterTSync, uintptr(unsafe.Pointer(&prog))); errno != 0 {
		return fmt.Errorf("seccomp: %w", errno)
	}
	return nil
}

// VerifyHardened is used by the isolation probe: after HardenRunner the
// runner must be unable to start a program or open a socket.
func VerifyHardened(self string) error {
	if _, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0); err == nil {
		return fmt.Errorf("socket creation was not blocked")
	}
	if err := unix.Exec(self, []string{self, "-version"}, nil); err == nil {
		return fmt.Errorf("exec was not blocked")
	}
	return nil
}
