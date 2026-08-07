package version

import "strings"

var (
	Version          = "0.0.1"
	Build            = "dev"
	Commit           = "unknown"
	Date             = "unknown"
	ReleasePublicKey = ""
	// Agent release metadata is injected from the signed manifest bundled by
	// the Controller build. It intentionally does not mirror Controller data.
	AgentVersion  = "0.0.1"
	AgentBuild    = "dev"
	AgentCommit   = "unknown"
	AgentDate     = "unknown"
	KernelVersion = "0.0.1"
	KernelBuild   = "dev"
)

func String() string {
	return Version + " (build " + Build + ", commit " + Commit + ", built " + Date + ")"
}

func IsDev() bool {
	v := strings.ToLower(strings.TrimSpace(Version))
	b := strings.ToLower(strings.TrimSpace(Build))
	return strings.Contains(v, "dev") || b == "" || b == "dev"
}
