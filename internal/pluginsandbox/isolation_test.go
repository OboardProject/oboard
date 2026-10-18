package pluginsandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSandboxDiagnosticsRemainBounded(t *testing.T) {
	command := &Command{}
	prefix := "bwrap: creating new namespace failed: Operation not permitted\n"
	_, _ = fmt.Fprint(&command.stderr, prefix)
	var writers sync.WaitGroup
	for i := 0; i < 8; i++ {
		writers.Go(func() {
			payload := strings.Repeat("x", maxDiagnosticBytes*2)
			if n, err := command.stderr.Write([]byte(payload)); err != nil || n != len(payload) {
				t.Errorf("diagnostic overflow must be discarded without failing stderr: %d, %v", n, err)
			}
			_ = command.Diagnostics()
		})
	}
	writers.Wait()
	got := command.Diagnostics()
	if len(got) != maxDiagnosticBytes || !strings.HasPrefix(got, prefix) {
		t.Fatalf("lost startup diagnostics or exceeded limit: %q", got)
	}
}

func TestOOMKilledRequiresCgroupEvidence(t *testing.T) {
	group := t.TempDir()
	command := &Command{group: group}
	for _, tc := range []struct {
		events string
		killed bool
	}{
		{"low 0\nhigh 0\nmax 3\noom 1\noom_kill 0\noom_group_kill 0\n", false},
		{"oom_kill 1\noom_group_kill 0\n", true},
		{"oom_kill 0\noom_group_kill 1\n", true},
	} {
		if err := os.WriteFile(filepath.Join(group, "memory.events"), []byte(tc.events), 0600); err != nil {
			t.Fatal(err)
		}
		if got := command.OOMKilled(); got != tc.killed {
			t.Fatalf("OOMKilled = %v for %q", got, tc.events)
		}
	}
}

func TestSandboxHasOnlyWorkerMountAndRPCDescriptors(t *testing.T) {
	args := sandboxArgs("/opt/private/worker", "-runner")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--unshare-all", "--unshare-user", "--disable-userns", "--cap-drop ALL", "--clearenv", "--ro-bind /opt/private/worker /worker", "--size 8388608 --tmpfs /tmp", "--remount-ro /", "/worker -runner"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s: %s", want, joined)
		}
	}
	mounts := 0
	for i, arg := range args {
		if arg == "--ro-bind" {
			mounts++
			if args[i+1] != "/opt/private/worker" {
				t.Fatal("host directory exposed")
			}
		}
	}
	if mounts != 1 {
		t.Fatal("unexpected host mounts")
	}
	probeArgs := sandboxArgs("/opt/private/worker", "-isolation-probe")
	if strings.Join(args[:len(args)-1], " ") != strings.Join(probeArgs[:len(probeArgs)-1], " ") {
		t.Fatal("runner and probe must use the same bubblewrap options")
	}
	if strings.Contains(joined, "--preserve-fds") {
		t.Fatal("bubblewrap does not support --preserve-fds; RPC must use stdio")
	}
}

func TestRunnerCgroupLimitsFailClosed(t *testing.T) {
	controls := map[string]string{"cgroup.kill": "", "memory.max": "67108864", "memory.swap.max": "0", "memory.oom.group": "1", "pids.max": maxRunnerTasks, "cpu.max": "100000 100000"}
	for _, missing := range []string{"", "cgroup.kill", "memory.max", "memory.swap.max", "memory.oom.group", "pids.max", "cpu.max"} {
		t.Run("missing-"+missing, func(t *testing.T) {
			root := t.TempDir()
			for name := range controls {
				if name != missing {
					if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := configureRunnerCgroup(root, 64)
			if missing != "" {
				if err == nil {
					t.Fatal("missing cgroup control accepted")
				}
				if _, err := os.Stat(filepath.Join(root, missing)); !os.IsNotExist(err) {
					t.Fatal("created a fake cgroup control")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range controls {
				got, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(got) != want {
					t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
				}
			}
		})
	}
}

func TestIsolationCannotBeBypassedByTestEnvironment(t *testing.T) {
	t.Setenv("OBOARD_PLUGIN_TEST_ISOLATION", "1")
	t.Setenv("OBOARD_PLUGIN_DISABLE_ISOLATION", "1")
	self, _ := os.Executable()
	if _, err := IsolatedCommand(context.Background(), self, 64, "-runner"); err == nil {
		t.Fatal("disabled isolation accepted")
	}
	if ProbeIsolation().Available {
		t.Fatal("disabled isolation reported available")
	}
}
