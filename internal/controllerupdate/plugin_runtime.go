package controllerupdate

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/OboardProject/oboard/deploy"
)

func (s *Service) pluginServicePaths() (string, string) {
	return filepath.Join(s.config.SystemdUnitDir, "oboard-plugin-worker.service"), filepath.Join(s.config.OpenRCServiceDir, "oboard-plugin-worker")
}

func (s *Service) pluginRuntimeInstalled() bool {
	marker := filepath.Join(filepath.Dir(s.config.BinaryEnvPath), "plugin-runtime.wanted")
	if info, err := os.Stat(marker); err != nil || !info.Mode().IsRegular() {
		return false
	}
	systemd, openrc := s.pluginServicePaths()
	for _, path := range []string{systemd, openrc} {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// Retiring a script runtime never opts into the plugin runtime. Failure to
// stop or disable the previous worker aborts cleanup rather than orphaning it.
func (s *Service) retireScriptRuntime(ctx context.Context) error {
	for _, item := range []struct {
		path     string
		commands [][]string
	}{
		{filepath.Join(s.config.SystemdUnitDir, "oboard-script-worker.service"), [][]string{{"systemctl", "disable", "--now", "oboard-script-worker.service"}}},
		{filepath.Join(s.config.OpenRCServiceDir, "oboard-script-worker"), [][]string{{"rc-service", "oboard-script-worker", "stop"}, {"rc-update", "del", "oboard-script-worker", "default"}}},
	} {
		if _, err := os.Lstat(item.path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		for _, command := range item.commands {
			if command[0] == "rc-update" {
				link := filepath.Join(filepath.Dir(s.config.OpenRCServiceDir), "runlevels/default/oboard-script-worker")
				if _, err := os.Lstat(link); os.IsNotExist(err) {
					continue
				} else if err != nil {
					return err
				}
			}
			if err := s.config.RunCommand(ctx, command[0], command[1:]...); err != nil {
				return fmt.Errorf("retire script runtime: %w", err)
			}
		}
		if err := os.Remove(item.path); err != nil {
			if !errors.Is(err, syscall.EROFS) {
				return err
			}
			// Older updater service sandboxes mount /etc read-only. The service is
			// already stopped and disabled; the installer removes the inert unit.
			log.Printf("retired script worker unit cleanup deferred to installer: read-only service directory")
		}
		if strings.HasSuffix(item.path, ".service") {
			if err := s.config.RunCommand(ctx, "systemctl", "daemon-reload"); err != nil {
				return err
			}
		}
	}
	for _, path := range []string{filepath.Join(filepath.Dir(s.config.BinaryEnvPath), "script-runtime.wanted"), filepath.Join(filepath.Dir(s.config.ControllerBinary), "oboard-script-worker")} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Service) refreshPluginService(ctx context.Context) error {
	if !s.pluginRuntimeInstalled() {
		return nil
	}
	root := filepath.Dir(s.config.ControllerBinary)
	if normalized, ok := normalizeInstallDir(root); !ok || normalized != root {
		return fmt.Errorf("invalid plugin installation root")
	}
	systemd, openrc := s.pluginServicePaths()
	for _, item := range []struct {
		path, template string
		mode           os.FileMode
	}{
		{systemd, deploy.PluginSystemd, 0644}, {openrc, deploy.PluginOpenRC, 0755},
	} {
		if _, err := os.Stat(item.path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		pending := item.path + ".update-new"
		file, err := os.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, item.mode)
		if err != nil {
			return err
		}
		_, writeErr := file.WriteString(strings.ReplaceAll(item.template, "/opt/oboard", root))
		if writeErr == nil {
			writeErr = file.Chmod(item.mode)
		}
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			os.Remove(pending)
			return fmt.Errorf("write plugin service: %v %v", writeErr, closeErr)
		}
		if err := os.Rename(pending, item.path); err != nil {
			os.Remove(pending)
			return err
		}
		if item.path == systemd {
			if err := s.config.RunCommand(ctx, "systemctl", "daemon-reload"); err != nil {
				return err
			}
		}
	}
	return nil
}
