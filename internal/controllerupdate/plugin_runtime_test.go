package controllerupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetireRealScriptRuntimeLayout(t *testing.T) {
	for _, manager := range []string{"systemd", "openrc"} {
		for _, fail := range []bool{false, true} {
			t.Run(manager+map[bool]string{true: "-failure", false: "-success"}[fail], func(t *testing.T) {
				root := t.TempDir()
				cfg := ServiceConfig{ControllerBinary: filepath.Join(root, "oboard-controller"), BinaryEnvPath: filepath.Join(root, "config/controller.env"), SystemdUnitDir: filepath.Join(root, "systemd"), OpenRCServiceDir: filepath.Join(root, "init.d")}
				unit := filepath.Join(cfg.SystemdUnitDir, "oboard-script-worker.service")
				if manager == "openrc" {
					unit = filepath.Join(cfg.OpenRCServiceDir, "oboard-script-worker")
				}
				marker := filepath.Join(root, "config/script-runtime.wanted")
				binary := filepath.Join(root, "oboard-script-worker")
				for _, path := range []string{unit, marker, binary} {
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("previous runtime"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				fixture, err := os.ReadFile("testdata/script-worker-50824f99." + manager)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(unit, []byte(strings.ReplaceAll(string(fixture), "/opt/oboard", root)), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, nil, 0644); err != nil {
					t.Fatal(err)
				}
				if manager == "openrc" {
					link := filepath.Join(root, "runlevels/default/oboard-script-worker")
					if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(unit, link); err != nil {
						t.Fatal(err)
					}
				}
				var calls []string
				cfg.RunCommand = func(_ context.Context, name string, args ...string) error {
					calls = append(calls, name+" "+strings.Join(args, " "))
					if fail {
						return errors.New("stop failed")
					}
					if name == "rc-update" {
						return os.Remove(filepath.Join(root, "runlevels/default/oboard-script-worker"))
					}
					return nil
				}
				s := &Service{config: cfg}
				err = s.retireScriptRuntime(context.Background())
				if fail {
					if err == nil {
						t.Fatal("ignored stop failure")
					}
					for _, path := range []string{unit, marker, binary} {
						if _, err := os.Stat(path); err != nil {
							t.Fatal("removed runtime asset after failed stop")
						}
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{unit, marker, binary, filepath.Join(root, "config/plugin-runtime.wanted"), filepath.Join(root, "oboard-plugin-worker")} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("unexpected runtime asset %s: %v", path, err)
					}
				}
				if len(calls) != 2 {
					t.Fatalf("stop and disable/reload not executed: %v", calls)
				}
				if err := s.retireScriptRuntime(context.Background()); err != nil || len(calls) != 2 {
					t.Fatal("cleanup is not idempotent")
				}
			})
		}
	}
}

func TestPluginServiceRefreshDoesNotEnableRuntime(t *testing.T) {
	root := t.TempDir()
	s := &Service{config: ServiceConfig{ControllerBinary: "/opt/oboard-custom/oboard-controller", BinaryEnvPath: filepath.Join(root, "controller.env"), SystemdUnitDir: root, OpenRCServiceDir: filepath.Join(root, "init.d")}}
	var calls []string
	s.config.RunCommand = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	unit := filepath.Join(root, "oboard-plugin-worker.service")
	if err := os.WriteFile(unit, []byte("unchanged"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshPluginService(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(unit)
	if string(raw) != "unchanged" || len(calls) != 0 {
		t.Fatal("service without opt-in changed")
	}
	if err := os.WriteFile(filepath.Join(root, "plugin-runtime.wanted"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshPluginService(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(unit)
	for _, want := range []string{"ExecStart=/opt/oboard-custom/oboard-plugin-worker", "Delegate=cpu memory pids", "MemoryMax=256M"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("updated service missing %s", want)
		}
	}
	if len(calls) != 1 || calls[0] != "systemctl daemon-reload" {
		t.Fatalf("unexpected enable/start command: %v", calls)
	}
	if strings.Contains(string(raw), "EnvironmentFile=") {
		t.Fatal("worker inherits Controller secrets")
	}
}

func TestPluginOptionalInstallationRequiresMarkerAndService(t *testing.T) {
	root := t.TempDir()
	s := &Service{config: ServiceConfig{BinaryEnvPath: filepath.Join(root, "controller.env"), SystemdUnitDir: root, OpenRCServiceDir: filepath.Join(root, "init.d")}}
	marker := filepath.Join(root, "plugin-runtime.wanted")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if s.pluginRuntimeInstalled() {
		t.Fatal("marker alone enables runtime")
	}
	unit := filepath.Join(root, "oboard-plugin-worker.service")
	if err := os.WriteFile(unit, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if !s.pluginRuntimeInstalled() {
		t.Fatal("opted-in service not detected")
	}
	os.Remove(marker)
	if s.pluginRuntimeInstalled() {
		t.Fatal("service alone enables runtime")
	}
}
