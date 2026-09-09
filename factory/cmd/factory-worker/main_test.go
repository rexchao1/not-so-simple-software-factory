package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityCommandUsesDerivedDataDirectory(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "cli-worker.toml")
	if err := os.WriteFile(configPath, []byte(`name = "cli-worker"`), 0o600); err != nil {
		t.Fatal(err)
	}

	runIdentity := func() string {
		t.Helper()
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		oldArgs, oldStdout := os.Args, os.Stdout
		os.Args = []string{"factory-worker", "identity", "--config", configPath}
		os.Stdout = writer
		err = run()
		os.Args, os.Stdout = oldArgs, oldStdout
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			reader.Close()
			t.Fatal(err)
		}
		output, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(output))
	}

	first := runIdentity()
	second := runIdentity()
	if first == "" || first != second {
		t.Fatalf("identity output changed: %q != %q", first, second)
	}
	body, err := os.ReadFile(filepath.Join(root, "workers", "cli-worker", "worker-id"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(body)) != first {
		t.Fatalf("worker-id does not match identity output %q", first)
	}
}

func TestExplicitConfigSelectionBypassesLegacyDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FACTORY_DATA_HOME", "")
	t.Setenv("FACTORY_WORKER_CONFIG", "")
	t.Setenv("FACTORY_V2_DATA_HOME", "")
	t.Setenv("FACTORY_V2_WORKER_CONFIG", "")
	legacyRoot := filepath.Join(home, ".factory-v2")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "worker.toml"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := validateDefaultConfigSelection(true); err != nil {
		t.Fatalf("explicit config selection was blocked: %v", err)
	}
	if err := validateDefaultConfigSelection(false); err == nil ||
		!strings.Contains(err.Error(), "preview worker state") {
		t.Fatalf("implicit config selection error = %v", err)
	}
}

func TestConfigArgumentDetection(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.String("config", "/default.toml", "")
	if err := flags.Parse([]string{"--config", "/explicit.toml"}); err != nil {
		t.Fatal(err)
	}
	if !flagExplicit(flags, "config") {
		t.Fatal("--config was not detected")
	}

	for _, arguments := range [][]string{
		{"attempt-id", "--config", "/explicit.toml"},
		{"attempt-id", "--config=/explicit.toml"},
	} {
		if !cleanupConfigExplicit(arguments) {
			t.Fatalf("cleanup config was not detected in %v", arguments)
		}
	}
	if cleanupConfigExplicit([]string{"attempt-id"}) {
		t.Fatal("cleanup config reported explicit without an argument")
	}
}
