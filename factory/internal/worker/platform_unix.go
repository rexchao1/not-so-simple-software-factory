//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package worker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func ensureSupportedPlatform() error { return nil }

func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func lockFile(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return errors.New("another factory-worker already owns this data directory")
		}
		return err
	}
	return nil
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func configureNewProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func configureExistingProcessGroup(command *exec.Cmd, processGroupID int) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: processGroupID}
}

func processGroupID(pid int) (int, error) {
	return unix.Getpgid(pid)
}

func processIdentity(pid int) (string, error) {
	command := exec.Command("ps", "-o", "lstart=", "-o", "command=", "-p", strconv.Itoa(pid))
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("inspect process %d: %w", pid, err)
	}
	identity := strings.TrimSpace(string(output))
	if identity == "" {
		return "", fmt.Errorf("process %d has no observable identity", pid)
	}
	return identity, nil
}

func verifyProcessIdentity(pid int, expected string) error {
	actual, err := processIdentity(pid)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("process %d identity changed", pid)
	}
	return nil
}

func signalProcessGroup(processGroupID int, signal unix.Signal) error {
	err := unix.Kill(-processGroupID, signal)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func forceStopStartedProcessGroup(processGroupID int) error {
	err := unix.Kill(-processGroupID, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func processGroupAlive(processGroupID int) bool {
	err := unix.Kill(-processGroupID, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

func stopOwnedProcessGroup(pid int, identity string, grace time.Duration) error {
	if err := verifyProcessIdentity(pid, identity); err != nil {
		return fmt.Errorf("refuse to signal unverified process group: %w", err)
	}
	if err := signalProcessGroup(pid, unix.SIGTERM); err != nil {
		return fmt.Errorf("terminate process group %d: %w", pid, err)
	}
	deadline := time.Now().Add(grace)
	for grace > 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		if !processGroupAlive(pid) {
			return nil
		}
	}
	if err := verifyProcessIdentity(pid, identity); err != nil {
		if !processGroupAlive(pid) {
			return nil
		}
		return fmt.Errorf("refuse to kill unverified process group: %w", err)
	}
	if err := signalProcessGroup(pid, unix.SIGKILL); err != nil {
		return fmt.Errorf("kill process group %d: %w", pid, err)
	}
	return nil
}
