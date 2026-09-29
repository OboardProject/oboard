package pluginsandbox

import (
	"fmt"
	"syscall"

	"golang.org/x/net/bpf"
)

const (
	seccompRetKillProcess = 0x80000000
	seccompRetErrno       = 0x00050000
	seccompRetAllow       = 0x7fff0000
	cloneThreadFlag       = 0x00010000
)

// buildSeccompProgram denies the given syscalls with EPERM and allows clone
// only with CLONE_THREAD, which the Go runtime uses for threads; fork-style
// clones are refused. A foreign architecture is killed outright.
func buildSeccompProgram(arch uint32, denied []uint32, cloneNr uint32) ([]bpf.RawInstruction, error) {
	n := len(denied)
	if n > 200 {
		return nil, fmt.Errorf("too many denied syscalls")
	}
	errno := uint32(seccompRetErrno | uint32(syscall.EPERM))
	program := []bpf.Instruction{
		bpf.LoadAbsolute{Off: 4, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: arch, SkipTrue: 1},
		bpf.RetConstant{Val: seccompRetKillProcess},
		bpf.LoadAbsolute{Off: 0, Size: 4},
	}
	for i, nr := range denied {
		program = append(program, bpf.JumpIf{Cond: bpf.JumpEqual, Val: nr, SkipTrue: uint8(n + 2 - i)})
	}
	program = append(program,
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: cloneNr, SkipFalse: 3},
		bpf.LoadAbsolute{Off: 16, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: cloneThreadFlag, SkipTrue: 1},
		bpf.RetConstant{Val: errno},
		bpf.RetConstant{Val: seccompRetAllow},
	)
	return bpf.Assemble(program)
}
