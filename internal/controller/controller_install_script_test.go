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
		"generate_admin_password",
		"超级管理员密码：$BOOTSTRAP_ADMIN_PASSWORD_VALUE",
		"该密码只显示这一次",
		"登录后请立即修改密码",
		"clear_bootstrap_admin_password",
		"unset_controller_env_value",
		"wait_for_controller_ready",
		"prepare_controller_env",
		"OBOARD_BASE_PATH",
		"install_agent_from_controller",
		"不会互相覆盖",
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
		"prepare_controller_updater_runtime",
		"wait_for_controller_updater",
		"curl --unix-socket /run/oboard/controller-updater.sock",
		"/var/lib/oboard/controller-update",
		"uninstall_controller",
		"OBoard 主控已卸载",
		"配置和数据已保留",
		"OBOARD_PURGE_DATA",
		"resolve_purge_data",
		"是否同时删除主控的配置和数据",
		"删除请直接回车，保留请输入 n [Y/n]",
		"当前无法交互确认，已保留",
		"当前暂无可用的稳定版，将安装最新开发版",
		"安装包下载失败",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("controller installer missing %q", want)
		}
	}
	if strings.Contains(text, "首次登录密码：admin") {
		t.Fatal("controller installer still advertises a well-known default password")
	}
	if strings.Contains(text, "grep -A2 'first administrator'") {
		t.Fatal("controller installer still sends operators to the service log for the bootstrap password")
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

func TestControllerUpdaterUnitBinaryWritePaths(t *testing.T) {
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
	want := "ReadWritePaths=/run/oboard /var/lib/oboard /opt/oboard /usr/local/bin"
	if !strings.Contains(text, want) {
		t.Fatalf("updater unit missing binary installation write paths %q", want)
	}
	for _, removed := range []string{"docker", "-/var/lib/oboard", "-/opt/oboard", "/etc/systemd/system"} {
		if strings.Contains(strings.ToLower(text), removed) {
			t.Fatalf("updater unit still contains removed path or dependency %q", removed)
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

	for _, script := range []string{"scripts/install.sh"} {
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
