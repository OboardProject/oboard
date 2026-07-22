package controller

import (
	"testing"

	"github.com/OboardProject/oboard/internal/version"
)

func TestCurrentVersionInfoUsesBundledAgentMetadata(t *testing.T) {
	originalAgentVersion, originalAgentBuild := version.AgentVersion, version.AgentBuild
	originalKernelVersion, originalKernelBuild := version.KernelVersion, version.KernelBuild
	defer func() {
		version.AgentVersion, version.AgentBuild = originalAgentVersion, originalAgentBuild
		version.KernelVersion, version.KernelBuild = originalKernelVersion, originalKernelBuild
	}()

	version.AgentVersion, version.AgentBuild = "dev-agent", "agent-build"
	version.KernelVersion, version.KernelBuild = "kernel-release", "kernel-build"
	info := (&Server{}).currentVersionInfo()
	if info.AgentExpectedVersion != "dev-agent" || info.AgentExpectedBuild != "agent-build" {
		t.Fatalf("Agent metadata = %q/%q", info.AgentExpectedVersion, info.AgentExpectedBuild)
	}
	if info.KernelVersion != "kernel-release" || info.KernelBuild != "kernel-build" {
		t.Fatalf("kernel metadata = %q/%q", info.KernelVersion, info.KernelBuild)
	}
}
