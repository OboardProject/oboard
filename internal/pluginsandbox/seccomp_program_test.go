package pluginsandbox

import (
	"encoding/binary"
	"syscall"
	"testing"

	"golang.org/x/net/bpf"
)

// Syscall numbers of linux/amd64; the filter logic is architecture-neutral.
const (
	testArch     = 0xc000003e
	testRead     = 0
	testSocket   = 41
	testClone    = 56
	testExecve   = 59
	testPtrace   = 101
	testClone3   = 435
	testCloneVM  = 0x100
	testSigchld  = 17
	testThreadFl = 0x100 | 0x200 | 0x400 | 0x800 | cloneThreadFlag | 0x40000
)

// seccompInput encodes seccomp_data as the filter reads it: every load is a
// 32-bit word, so each word is written in the VM's load order.
func seccompInput(arch, nr, arg0 uint32) []byte {
	data := make([]byte, 64)
	binary.BigEndian.PutUint32(data[0:], nr)
	binary.BigEndian.PutUint32(data[4:], arch)
	binary.BigEndian.PutUint32(data[16:], arg0)
	return data
}

func TestSeccompFilterDecisions(t *testing.T) {
	denied := []uint32{testExecve, testSocket, testPtrace, testClone3}
	raw, err := buildSeccompProgram(testArch, denied, testClone)
	if err != nil {
		t.Fatal(err)
	}
	instructions, ok := bpf.Disassemble(raw)
	if !ok {
		t.Fatal("seccomp program does not disassemble")
	}
	vm, err := bpf.NewVM(instructions)
	if err != nil {
		t.Fatal(err)
	}
	deny := int(seccompRetErrno | uint32(syscall.EPERM))
	cases := []struct {
		name          string
		arch, nr, arg uint32
		want          int
	}{
		{"execve", testArch, testExecve, 0, deny},
		{"socket", testArch, testSocket, 0, deny},
		{"ptrace", testArch, testPtrace, 0, deny},
		{"clone3", testArch, testClone3, 0, deny},
		{"fork-style clone", testArch, testClone, testSigchld, deny},
		{"vm-only clone", testArch, testClone, testCloneVM, deny},
		{"thread clone", testArch, testClone, testThreadFl, seccompRetAllow},
		{"read", testArch, testRead, 0, seccompRetAllow},
		{"foreign architecture", 0x40000003, testRead, 0, seccompRetKillProcess},
	}
	for _, tc := range cases {
		got, err := vm.Run(seccompInput(tc.arch, tc.nr, tc.arg))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: filter returned %#x, want %#x", tc.name, got, tc.want)
		}
	}
	if _, err := buildSeccompProgram(testArch, make([]uint32, 201), testClone); err == nil {
		t.Fatal("an oversized deny list must be refused")
	}
}
