package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkerIDCreatesAndReusesStableIdentity(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "worker")
	config := Config{
		Server:        "http://127.0.0.1:7337",
		Name:          "identity-test",
		MaxConcurrent: 1,
		DataDirectory: dataDirectory,
		Repositories:  map[string]RepositoryConfig{"factory": {Path: t.TempDir()}},
	}

	first, err := ResolveWorkerID(config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveWorkerID(config)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !uuidPattern.MatchString(first) {
		t.Fatalf("worker IDs = %q and %q", first, second)
	}
	info, err := os.Lstat(filepath.Join(dataDirectory, "worker-id"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("worker ID mode = %o", info.Mode().Perm())
	}
}
