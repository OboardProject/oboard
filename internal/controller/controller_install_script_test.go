package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestControllerInstallScriptUserGuidanceAndSyntax(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test file")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "scripts", "install.sh"))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{
		`COMPONENT=${COMPONENT:-${1:-controller}}`,
		"OBoard 主控安装 / 更新完成",
		"面板地址",
		"设置超级管理员",
		"超级管理员账号：",
		"自动加入“管理员组”",
		"不能在面板中删除",
		"configure_bootstrap_admin",
		"OBOARD_ADMIN_USERNAME",
		"OBOARD_ADMIN_PASSWORD",
		"超级管理员密码：由主控首次启动时生成并打印到服务日志",
		"登录后请立即修改密码",
		"systemctl status oboard-controller",
		"rc-service oboard-controller status",
		"输入 obag 打开管理菜单",
		"prepare_controller_env",
		"OBOARD_BASE_PATH",
		"install_agent_from_controller",
		"Controller 和 Agent 相互独立，也可以安装在同一台服务器上",
		"不会互相覆盖",
		"oboard-controller；Agent 服务：oboard-agent、oboard-sb",
		"COMPONENT=agent",
		"make_install_tmp",
		"OBOARD_TMPDIR",
		"pkg_install",
		"ensure_base_tools",
		"create_system_user",
		"detect_virt_hint",
		"centos",
		"rhel",
		"rocky",
		"almalinux",
		"OBOARD_UPDATE_CHANNEL",
		"oboard-controller-updater",
		"harden_controller_updater_unit",
		"prepare_controller_updater_runtime",
		"wait_for_controller_updater",
		"curl --unix-socket /run/oboard/controller-updater.sock",
		"/var/lib/oboard/controller-update",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("controller installer missing %q", want)
		}
	}
	if strings.Contains(text, "首次登录密码：admin") {
		t.Fatal("controller installer still advertises a well-known default password")
	}
	if strings.Contains(text, `install_component agent`) || strings.Contains(text, `install_component sb`) {
		t.Fatal("controller installer still installs Agent artifacts from the controller release")
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		cmd := exec.Command(bash, "-n", path)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("controller installer syntax error: %v\n%s", err, output)
		}
	}
}

func TestControllerUpdaterUnitOptionalInstallPaths(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test file")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "deploy", "systemd", "oboard-controller-updater.service"))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, optional := range []string{"-/var/lib/oboard", "-/opt/oboard", "-/opt/oboard-docker", "-/etc/oboard"} {
		if !strings.Contains(text, optional) {
			t.Fatalf("updater unit must mark %s as optional so binary and Docker installs can start with only their own paths", optional)
		}
	}
	for _, required := range []string{
		"ReadWritePaths=/run/oboard /var/lib/oboard ",
		"ReadWritePaths=/run/oboard /var/lib/oboard /opt/oboard /opt/oboard-docker ",
		" /opt/oboard /opt/oboard-docker ",
		" /etc/oboard /etc/systemd/system",
	} {
		if strings.Contains(text, required) {
			t.Fatalf("updater unit still requires a non-optional install path fragment %q", required)
		}
	}
}

func TestControllerUpdaterRuntimePreparationPreservesDataRoot(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	if _, err := exec.LookPath("install"); err != nil {
		t.Skip("install is unavailable")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test file")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	for _, script := range []string{"scripts/install.sh", "scripts/install-docker.sh"} {
		t.Run(script, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(script)))
			if err != nil {
				t.Fatal(err)
			}
			original := extractShellFunction(t, string(content), "prepare_controller_updater_runtime")
			rewrite := func(dataRoot, runtimeRoot string) string {
				function := strings.ReplaceAll(original, "/var/lib/oboard", shellQuote(dataRoot))
				function = strings.ReplaceAll(function, "/run/oboard", shellQuote(runtimeRoot))
				function = strings.ReplaceAll(function, "-o root -g oboard ", "")
				return strings.ReplaceAll(function, "-o root -g root ", "")
			}
			run := func(t *testing.T, function string) error {
				t.Helper()
				cmd := exec.Command(bash, "-c", function+"\nprepare_controller_updater_runtime")
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Logf("runtime preparation output:\n%s", output)
				}
				return err
			}

			t.Run("existing data root", func(t *testing.T) {
				temp := t.TempDir()
				dataRoot := filepath.Join(temp, "data")
				runtimeRoot := filepath.Join(temp, "run")
				if err := os.Mkdir(dataRoot, 0o711); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dataRoot, 0o711); err != nil {
					t.Fatal(err)
				}
				if err := run(t, rewrite(dataRoot, runtimeRoot)); err != nil {
					t.Fatal(err)
				}
				assertPathMode(t, dataRoot, 0o711)
				assertPathMode(t, runtimeRoot, 0o750)
				assertPathMode(t, filepath.Join(dataRoot, "controller-update"), 0o700)
			})

			t.Run("missing data root", func(t *testing.T) {
				temp := t.TempDir()
				dataRoot := filepath.Join(temp, "data")
				runtimeRoot := filepath.Join(temp, "run")
				if err := run(t, rewrite(dataRoot, runtimeRoot)); err != nil {
					t.Fatal(err)
				}
				assertPathMode(t, dataRoot, 0o750)
				assertPathMode(t, filepath.Join(dataRoot, "controller-update"), 0o700)
			})

			t.Run("symlink data root", func(t *testing.T) {
				temp := t.TempDir()
				target := filepath.Join(temp, "target")
				dataRoot := filepath.Join(temp, "data")
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dataRoot); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
				if err := run(t, rewrite(dataRoot, filepath.Join(temp, "run"))); err == nil {
					t.Fatal("runtime preparation accepted a symlink data root")
				}
			})
		})
	}
}

func extractShellFunction(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, name+"() {")
	if start < 0 {
		t.Fatalf("script is missing %s", name)
	}
	rest := script[start:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("script has an unterminated %s", name)
	}
	return rest[:end+2]
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func assertPathMode(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual := info.Mode().Perm(); actual != expected {
		t.Fatalf("%s mode = %04o, want %04o", path, actual, expected)
	}
}
