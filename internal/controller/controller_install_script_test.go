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
