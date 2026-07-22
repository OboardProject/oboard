package version

import "strings"

var (
	Version          = "0.1.0-dev"
	Build            = "dev"
	Commit           = "unknown"
	Date             = "unknown"
	ReleasePublicKey = ""
)

func String() string {
	return Version + " (build " + Build + ", commit " + Commit + ", built " + Date + ")"
}

func IsDev() bool {
	v := strings.ToLower(strings.TrimSpace(Version))
	b := strings.ToLower(strings.TrimSpace(Build))
	return strings.Contains(v, "dev") || b == "" || b == "dev"
}
