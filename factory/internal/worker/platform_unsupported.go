//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package worker

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func ensureSupportedPlatform() error {
	return errors.New("factory-worker is supported only on Unix")
}

func ShutdownSignals() []os.Signal                           { return []os.Signal{os.Interrupt} }
func lockFile(*os.File) error                                { return ensureSupportedPlatform() }
func unlockFile(*os.File) error                              { return nil }
func syncDirectory(string) error                             { return ensureSupportedPlatform() }
func configureNewProcessGroup(*exec.Cmd)                     {}
func configureExistingProcessGroup(*exec.Cmd, int)           {}
func processGroupID(int) (int, error)                        { return 0, ensureSupportedPlatform() }
func processIdentity(int) (string, error)                    { return "", ensureSupportedPlatform() }
func verifyProcessIdentity(int, string) error                { return ensureSupportedPlatform() }
func processGroupAlive(int) bool                             { return false }
func forceStopStartedProcessGroup(int) error                 { return ensureSupportedPlatform() }
func stopOwnedProcessGroup(int, string, time.Duration) error { return ensureSupportedPlatform() }
