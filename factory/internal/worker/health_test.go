package worker

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestHealthProbesRunConcurrently(t *testing.T) {
	const probeCount = 5
	started := make(chan struct{}, probeCount)
	release := make(chan struct{})
	done := make(chan struct{})
	var closeOnce sync.Once
	closeRelease := func() { closeOnce.Do(func() { close(release) }) }
	defer closeRelease()

	probes := make([]func(), probeCount)
	for index := range probes {
		probes[index] = func() {
			started <- struct{}{}
			<-release
		}
	}
	go func() {
		runHealthProbes(probes...)
		close(done)
	}()

	for index := 0; index < probeCount; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("only %d of %d health probes started before another probe finished", index, probeCount)
		}
	}
	closeRelease()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent health probes did not finish")
	}
}

func TestRuntimeCapabilityUsesOneDeadlineForVersionAndAuthentication(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "slow-codex")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  sleep 0.5
  echo "codex test"
  exit 0
fi
sleep 3
echo "authenticated"
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	capability := runtimeCapabilityWithin(
		context.Background(), protocol.RuntimeCodex, executable, 1500*time.Millisecond,
	)
	elapsed := time.Since(started)
	if capability.Status != protocol.CapabilityUnauthenticated {
		t.Fatalf("slow auth capability = %#v", capability)
	}
	if elapsed >= 1900*time.Millisecond {
		t.Fatalf("runtime probe used separate command deadlines: elapsed %s", elapsed)
	}
}
