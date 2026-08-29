//go:build !windows

package daemon

import "fmt"

func RequireSupervisedElevation() error { return nil }

func WaitForSupervisorStartGate() error { return nil }

func RunSupervisor(SupervisorConfig) error {
	return fmt.Errorf("the built-in daemon supervisor is only available on Windows")
}
