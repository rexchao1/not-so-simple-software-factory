package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFakeCodex(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\nif [ \"${1:-}\" = \"--version\" ]; then echo 'codex-test'; exit 0; fi\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func testOptions(codexPath string) Options {
	return Options{
		GitExecutable: "git", GitHubExecutable: filepath.Join(filepath.Dir(codexPath), "unavailable-gh"),
		RuntimeExecutable: codexPath, WorkerVersion: "test", PollInterval: 20 * time.Millisecond,
		HealthInterval: 300 * time.Millisecond, RegistrationInterval: 25 * time.Millisecond,
		LeaseRenewInterval: 100 * time.Millisecond, LeaseRetryInterval: 50 * time.Millisecond,
		TransportBackoffMin: 20 * time.Millisecond, TransportBackoffMax: 100 * time.Millisecond,
		ShutdownTimeout: 15 * time.Second,
	}
}
