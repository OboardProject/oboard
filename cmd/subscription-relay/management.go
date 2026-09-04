package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/subrelay"
	"github.com/OboardProject/oboard/internal/version"
)

type enrollmentResponse struct {
	RelayID       int64  `json:"relay_id"`
	RelayToken    string `json:"relay_token"`
	SigningSecret string `json:"signing_secret"`
}

type heartbeatResponse struct {
	Action        string `json:"action"`
	TargetVersion string `json:"target_version"`
	TargetBuild   string `json:"target_build"`
}

func runEnrollment(args []string) error {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	controllerValue := flags.String("controller", env("OBOARD_CONTROLLER_URL", ""), "Controller base URL")
	token := flags.String("token", env("OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN", ""), "one-time enrollment token")
	envOutput := flags.String("env-output", "/opt/oboard-subscription-relay/relay.env", "managed environment file")
	allowHTTP := flags.Bool("allow-http", false, "allow HTTP for local tests")
	if err := flags.Parse(args); err != nil {
		return err
	}
	controllerURL, err := validateUpstream(*controllerValue, *allowHTTP)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*token) == "" {
		return errors.New("OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN is required")
	}
	payload, _ := json.Marshal(map[string]string{"enrollment_token": *token})
	endpoint := controllerEndpoint(controllerURL, "/api/v1/subscription-relay/enroll")
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("relay enrollment: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("relay enrollment returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var enrolled enrollmentResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&enrolled); err != nil {
		return err
	}
	if enrolled.RelayID <= 0 || enrolled.RelayToken == "" || enrolled.SigningSecret == "" {
		return errors.New("Controller returned incomplete relay credentials")
	}
	content := fmt.Sprintf("OBOARD_CONTROLLER_URL=%s\nOBOARD_SUBSCRIPTION_RELAY_ID=%d\nOBOARD_SUBSCRIPTION_RELAY_TOKEN=%s\nOBOARD_SUBSCRIPTION_RELAY_SECRET=%s\n", strings.TrimRight(controllerURL.String(), "/"), enrolled.RelayID, enrolled.RelayToken, enrolled.SigningSecret)
	return writePrivateFile(*envOutput, []byte(content))
}

func runUpdater(args []string) error {
	flags := flag.NewFlagSet("updater", flag.ContinueOnError)
	interval := flags.Duration("interval", 30*time.Second, "heartbeat interval")
	allowHTTP := flags.Bool("allow-http", false, "allow HTTP for local tests")
	if err := flags.Parse(args); err != nil {
		return err
	}
	controllerURL, err := validateUpstream(env("OBOARD_CONTROLLER_URL", ""), *allowHTTP)
	if err != nil {
		return err
	}
	relayID := env("OBOARD_SUBSCRIPTION_RELAY_ID", "")
	relayToken := env("OBOARD_SUBSCRIPTION_RELAY_TOKEN", "")
	relaySecret := env("OBOARD_SUBSCRIPTION_RELAY_SECRET", "")
	if _, err := strconv.ParseInt(relayID, 10, 64); err != nil || relayToken == "" || subrelay.ValidateSecret(relaySecret) != nil {
		return errors.New("managed relay credentials are invalid")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	var updateError *string
	for {
		action, err := sendHeartbeat(ctx, client, controllerURL, relayID, relayToken, relaySecret, updateError)
		if err == nil {
			updateError = nil
			if action.Action == "update" {
				if err := installRelayUpdate(ctx, client, controllerURL, action.TargetVersion); err != nil {
					message := err.Error()
					updateError = &message
				} else {
					return nil
				}
			}
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func sendHeartbeat(ctx context.Context, client *http.Client, controllerURL *url.URL, relayID, relayToken, relaySecret string, updateError *string) (heartbeatResponse, error) {
	payload := struct {
		Version        string  `json:"version"`
		Build          string  `json:"build"`
		Commit         string  `json:"commit"`
		OS             string  `json:"os"`
		Arch           string  `json:"arch"`
		ServiceManager string  `json:"service_manager"`
		UpdateError    *string `json:"update_error,omitempty"`
	}{Version: version.Version, Build: version.Build, Commit: version.Commit, OS: runtime.GOOS, Arch: runtime.GOARCH, ServiceManager: detectServiceManager(), UpdateError: updateError}
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, controllerEndpoint(controllerURL, "/api/v1/subscription-relay/heartbeat"), bytes.NewReader(body))
	if err != nil {
		return heartbeatResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+relayToken)
	if err := signControlRequest(request, relayID, relaySecret, body); err != nil {
		return heartbeatResponse{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return heartbeatResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return heartbeatResponse{}, fmt.Errorf("relay heartbeat returned %s", response.Status)
	}
	var result heartbeatResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return heartbeatResponse{}, err
	}
	return result, nil
}

func installRelayUpdate(ctx context.Context, client *http.Client, controllerURL *url.URL, targetVersion string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, controllerEndpoint(controllerURL, "/install/subscription-relay.sh"), nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download relay installer: %s", response.Status)
	}
	script, err := io.ReadAll(io.LimitReader(response.Body, 512<<10))
	if err != nil {
		return err
	}
	if len(script) == 0 || len(script) > 512<<10 {
		return errors.New("relay installer payload is invalid")
	}
	command := exec.CommandContext(ctx, "/bin/sh")
	command.Stdin = bytes.NewReader(script)
	command.Env = append(os.Environ(), "OBOARD_ACTION=update", "OBOARD_MANAGED_UPDATE=1", "OBOARD_CONTROLLER_URL="+strings.TrimRight(controllerURL.String(), "/"), "VERSION="+targetVersion)
	output, err := command.CombinedOutput()
	if len(output) > 0 {
		_, _ = os.Stdout.Write(output)
	}
	if err != nil {
		return fmt.Errorf("relay update failed: %w%s", err, relayUpdateOutputSuffix(output))
	}
	return nil
}

func relayUpdateOutputSuffix(output []byte) string {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	useful := make([]string, 0, 4)
	for i := len(lines) - 1; i >= 0 && len(useful) < 4; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || line == "----------------" || line == "------------------------" {
			continue
		}
		if line == "OBoard 订阅中继" || line == "OBoard 订阅中继操作未完成。" || line == "请根据上方提示处理后重试。" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.Contains(line, "/4]") {
			continue
		}
		if strings.HasPrefix(line, "主控地址：") || strings.HasPrefix(line, "安装目录：") || strings.HasPrefix(line, "监听地址：") || strings.HasPrefix(line, "目标版本：") || strings.HasPrefix(line, "环境：") {
			continue
		}
		if line == "正在更新，现有接入配置将保留。" || line == "正在开始安装。" {
			continue
		}
		useful = append(useful, line)
	}
	if len(useful) == 0 {
		return ""
	}
	for left, right := 0, len(useful)-1; left < right; left, right = left+1, right-1 {
		useful[left], useful[right] = useful[right], useful[left]
	}
	detail := strings.Join(useful, " · ")
	if len(detail) > 360 {
		detail = "…" + detail[len(detail)-359:]
	}
	return ": " + detail
}

func notifyUninstall(controllerURL, relayID, relayToken, relaySecret string) {
	target, err := validateUpstream(controllerURL, false)
	if err != nil || relayID == "" || relayToken == "" || subrelay.ValidateSecret(relaySecret) != nil {
		return
	}
	body := []byte(`{}`)
	request, _ := http.NewRequest(http.MethodPost, controllerEndpoint(target, "/api/v1/subscription-relay/uninstall"), bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+relayToken)
	if err := signControlRequest(request, relayID, relaySecret, body); err != nil {
		return
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err == nil {
		_ = response.Body.Close()
	}
}

func signControlRequest(request *http.Request, relayID, relaySecret string, body []byte) error {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := hex.EncodeToString(nonceBytes)
	request.Header.Set(subrelay.HeaderRelayID, relayID)
	request.Header.Set(subrelay.HeaderTimestamp, timestamp)
	request.Header.Set(subrelay.HeaderNonce, nonce)
	request.Header.Set(subrelay.HeaderSignature, subrelay.SignControl(relaySecret, relayID, request.Method, managedControlPath(request.URL.Path), timestamp, nonce, body))
	return nil
}

func managedControlPath(path string) string {
	for _, suffix := range []string{"/api/v1/subscription-relay/heartbeat", "/api/v1/subscription-relay/uninstall"} {
		if strings.HasSuffix(path, suffix) {
			return suffix
		}
	}
	return path
}

func controllerEndpoint(controllerURL *url.URL, suffix string) string {
	target := *controllerURL
	target.Path = strings.TrimRight(controllerURL.Path, "/") + suffix
	target.RawPath, target.RawQuery, target.Fragment = "", "", ""
	return target.String()
}

func writePrivateFile(path string, content []byte) error {
	if !filepath.IsAbs(path) {
		return errors.New("environment output path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func detectServiceManager() string {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return "systemd"
	}
	if _, err := exec.LookPath("rc-service"); err == nil {
		return "openrc"
	}
	return "unknown"
}
