package worker

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

type limitBuffer struct {
	mu        sync.Mutex
	bytes     []byte
	limit     int
	truncated bool
}

func newLimitBuffer(limit int) *limitBuffer {
	return &limitBuffer{limit: limit}
}

func (buffer *limitBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	available := buffer.limit - len(buffer.bytes)
	if available > 0 {
		count := len(value)
		if count > available {
			count = available
		}
		buffer.bytes = append(buffer.bytes, value[:count]...)
	}
	if len(value) > available {
		buffer.truncated = true
	}
	return len(value), nil
}

func (buffer *limitBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return string(append([]byte(nil), buffer.bytes...))
}

func (buffer *limitBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.bytes...)
}

func (buffer *limitBuffer) Truncated() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.truncated
}

func runCommand(ctx context.Context, executable, directory string, outputLimit int, arguments ...string) ([]byte, []byte, error) {
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	configureNewProcessGroup(command)
	stdout := newLimitBuffer(outputLimit)
	stderr := newLimitBuffer(outputLimit)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return nil, nil, err
	}
	pid := command.Process.Pid
	identity, identityErr := processIdentity(pid)
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		return stdout.Bytes(), stderr.Bytes(), err
	case <-ctx.Done():
		if identityErr == nil {
			_ = stopOwnedProcessGroup(pid, identity, time.Second)
		} else {
			_ = forceStopStartedProcessGroup(pid)
		}
		<-done
		return stdout.Bytes(), stderr.Bytes(), ctx.Err()
	}
}

func commandFailure(action string, stdout, stderr []byte, err error) error {
	detail := bytes.TrimSpace(stderr)
	if len(detail) == 0 {
		detail = bytes.TrimSpace(stdout)
	}
	if len(detail) == 0 {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, detail)
}
