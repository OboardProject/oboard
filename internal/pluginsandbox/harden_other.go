//go:build !linux || (!amd64 && !arm64)

package pluginsandbox

import "errors"

// HardenRunner refuses to run plugin code where the seccomp filter cannot be
// installed; plugins only execute inside the Linux sandbox.
func HardenRunner() error { return errors.New("plugin runner hardening requires Linux amd64 or arm64") }

func VerifyHardened(string) error {
	return errors.New("plugin runner hardening requires Linux amd64 or arm64")
}
