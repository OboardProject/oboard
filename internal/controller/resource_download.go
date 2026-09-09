package controller

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/version"
)

const resourceDownloadSourceSetting = "resource_download_source"

var resourceReleaseVersion = regexp.MustCompile(`^(?:v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?|dev-[0-9a-f]{12})$`)
var resourceReleaseBuild = regexp.MustCompile(`^[0-9]{14}$`)
var resourceDownloadClient = &http.Client{Timeout: 5 * time.Second}

func validateResourceDownloadSource(source string) error {
	if source != "controller" && source != "github" {
		return errors.New("resource_download_source must be controller or github")
	}
	return nil
}

func resourceReleaseTag(releaseVersion, build string) string {
	if !resourceReleaseVersion.MatchString(releaseVersion) {
		return ""
	}
	if strings.HasPrefix(releaseVersion, "dev-") {
		if !resourceReleaseBuild.MatchString(build) {
			return ""
		}
		return releaseVersion + "-" + build
	}
	return "v" + strings.TrimPrefix(releaseVersion, "v")
}

func resourceGitHubURL(name string) string {
	repo, releaseVersion, build := "OboardProject/oboard-agent", version.AgentVersion, version.AgentBuild
	switch name {
	case "oboard-agent-linux-amd64", "oboard-agent-linux-arm64", "oboard-sb-linux-amd64", "oboard-sb-linux-arm64", "oboard-realm-linux-amd64", "oboard-realm-linux-arm64":
	default:
		return ""
	}
	tag := resourceReleaseTag(releaseVersion, build)
	if tag == "" {
		return ""
	}
	return "https://github.com/" + repo + "/releases/download/" + tag + "/" + name
}

func (s *Server) redirectResourceDownload(w http.ResponseWriter, r *http.Request, name string) bool {
	if r.URL.Query().Get("source") == "controller" {
		return false
	}
	if s.runtimeSettings(r.Context())[resourceDownloadSourceSetting] != "github" && !strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"+name), "/downloads/github") {
		return false
	}
	target := resourceGitHubURL(name)
	if target == "" {
		return false
	}
	probe, err := http.NewRequestWithContext(r.Context(), http.MethodHead, target, nil)
	if err != nil {
		return false
	}
	response, err := resourceDownloadClient.Do(probe)
	if err != nil {
		return false
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	return true
}
