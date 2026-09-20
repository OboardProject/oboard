package plugin

import (
	"context"
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type IsolationStatus struct {
	Available    bool
	Mode, Reason string
}

// ProbeIsolation executes the same static worker in the same namespace and
// cgroup boundary as a run; installed tools alone are not proof of isolation.
func ProbeIsolation() IsolationStatus {
	self, err := os.Executable()
	if err != nil {
		return IsolationStatus{Mode: "unavailable", Reason: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, cleanup, err := IsolatedCommand(ctx, self, 64, "-isolation-probe")
	if err == nil {
		defer cleanup()
		err = cmd.Run()
	}
	if err != nil {
		return IsolationStatus{Mode: "unavailable", Reason: "plugin isolation unavailable: " + err.Error()}
	}
	return IsolationStatus{Available: true, Mode: "bubblewrap+cgroupv2"}
}

var delegation struct {
	sync.Mutex
	root string
}

func delegatedRoot() (string, error) {
	delegation.Lock()
	defer delegation.Unlock()
	if delegation.root != "" {
		return delegation.root, nil
	}
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	var root string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::/") {
			root = filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"))
			break
		}
	}
	if configured := os.Getenv("OBOARD_PLUGIN_CGROUP_ROOT"); configured != "" {
		if configured != "/sys/fs/cgroup/oboard-plugin-worker" || root != configured+"/supervisor" {
			return "", fmt.Errorf("invalid plugin cgroup delegation")
		}
		root = configured
	}
	// Never attempt to delegate the host root or an arbitrary parent cgroup.
	if root == "" || root == "/sys/fs/cgroup" {
		return "", fmt.Errorf("worker requires a dedicated delegated cgroup v2")
	}
	controllers, err := os.ReadFile(filepath.Join(root, "cgroup.controllers"))
	if err != nil {
		return "", err
	}
	for _, required := range []string{"memory", "pids", "cpu"} {
		if !strings.Contains(" "+strings.TrimSpace(string(controllers))+" ", " "+required+" ") {
			return "", fmt.Errorf("cgroup controller %s unavailable", required)
		}
	}
	supervisor := filepath.Join(root, "supervisor")
	if err := os.MkdirAll(supervisor, 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(supervisor, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("+memory +pids +cpu"), 0600); err != nil {
		return "", err
	}
	delegation.root = root
	return root, nil
}

// IsolatedCommand only supports the statically linked release worker: no host
// library directories, configuration, sockets, home or filesystem are mounted.
func IsolatedCommand(ctx context.Context, self string, memoryMiB int, mode string) (*exec.Cmd, func(), error) {
	if runtime.GOOS != "linux" || os.Getenv("OBOARD_PLUGIN_DISABLE_ISOLATION") == "1" {
		return nil, nil, fmt.Errorf("Linux isolation is unavailable or disabled")
	}
	if memoryMiB <= 0 || memoryMiB > DefaultRunnerMemoryMiB {
		return nil, nil, fmt.Errorf("invalid runner memory limit")
	}
	if mode != "-runner" && mode != "-isolation-probe" {
		return nil, nil, fmt.Errorf("invalid runner mode")
	}
	binary, err := elf.Open(self)
	if err != nil {
		return nil, nil, fmt.Errorf("static Linux worker required: %w", err)
	}
	defer binary.Close()
	for _, p := range binary.Progs {
		if p.Type == elf.PT_INTERP {
			return nil, nil, fmt.Errorf("dynamically linked workers are not supported")
		}
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, nil, err
	}
	root, err := delegatedRoot()
	if err != nil {
		return nil, nil, err
	}
	group, err := os.MkdirTemp(root, "run-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		_ = writeCgroupControl(group, "cgroup.kill", "1")
		for i := 0; i < 20; i++ {
			if err := os.Remove(group); err == nil || os.IsNotExist(err) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := configureRunnerCgroup(group, memoryMiB); err != nil {
		cleanup()
		return nil, nil, err
	}
	fd, err := os.Open(group)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, bwrap, sandboxArgs(self, mode)...)
	cmd.Env = []string{"LANG=C", "GOMAXPROCS=1"}
	if err := attachCgroup(cmd, fd); err != nil {
		fd.Close()
		cleanup()
		return nil, nil, err
	}
	return cmd, func() { fd.Close(); cleanup() }, nil
}

func writeCgroupControl(group, name, value string) error {
	file, err := os.OpenFile(filepath.Join(group, name), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(value)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func configureRunnerCgroup(group string, memoryMiB int) error {
	if _, err := os.Stat(filepath.Join(group, "cgroup.kill")); err != nil {
		return err
	}
	for name, value := range map[string]string{"memory.max": strconv.Itoa(memoryMiB << 20), "memory.swap.max": "0", "memory.oom.group": "1", "pids.max": "32", "cpu.max": "100000 100000"} {
		if err := writeCgroupControl(group, name, value); err != nil {
			return fmt.Errorf("set %s: %w", name, err)
		}
		actual, err := os.ReadFile(filepath.Join(group, name))
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(actual)) != value {
			return fmt.Errorf("cgroup limit %s did not take effect", name)
		}
	}
	return nil
}

func sandboxArgs(self, mode string) []string {
	args := []string{"--unshare-all", "--disable-userns", "--die-with-parent", "--new-session", "--cap-drop", "ALL", "--clearenv", "--setenv", "GOMAXPROCS", "1", "--ro-bind", self, "/worker", "--size", "8388608", "--tmpfs", "/tmp", "--proc", "/proc", "--dev", "/dev", "--remount-ro", "/", "--chdir", "/tmp"}
	if mode == "-runner" {
		args = append(args, "--preserve-fds", "2")
	}
	return append(args, "/worker", mode)
}
