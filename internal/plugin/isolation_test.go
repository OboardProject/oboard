package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxHasOnlyWorkerMountAndRPCDescriptors(t *testing.T) {
	args := sandboxArgs("/opt/private/worker", "-runner")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--unshare-all", "--disable-userns", "--cap-drop ALL", "--clearenv", "--ro-bind /opt/private/worker /worker", "--size 8388608 --tmpfs /tmp", "--remount-ro /", "--preserve-fds 2", "/worker -runner"} {
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
	if strings.Contains(strings.Join(sandboxArgs("/worker", "-isolation-probe"), " "), "--preserve-fds") {
		t.Fatal("probe must not inherit RPC")
	}
}

func TestRunnerCgroupLimitsFailClosed(t *testing.T) {
	controls := map[string]string{"cgroup.kill": "", "memory.max": "67108864", "memory.swap.max": "0", "memory.oom.group": "1", "pids.max": "32", "cpu.max": "100000 100000"}
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
	if _, _, err := IsolatedCommand(context.Background(), self, 64, "-runner"); err == nil {
		t.Fatal("disabled isolation accepted")
	}
	if ProbeIsolation().Available {
		t.Fatal("disabled isolation reported available")
	}
}
