package releasebuilder

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateInputsAndTargets(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, version := range []string{"v1.2.3", "v1.2.3-rc.1", "v0.0.0-test.issue-212"} {
		if err := validateInputs(version, commit); err != nil {
			t.Fatalf("valid inputs %s: %v", version, err)
		}
	}
	for _, version := range []string{"1.2.3", "v1.2", "v1.2.3-", "v1.2.3 bad"} {
		if err := validateInputs(version, commit); err == nil {
			t.Fatalf("invalid version %q was accepted", version)
		}
	}
	if err := validateInputs("v1.2.3", "short"); err == nil {
		t.Fatal("short commit was accepted")
	}
	if err := validateTargets(DefaultTargets); err != nil {
		t.Fatal(err)
	}
	if err := validateTargets([]Target{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "amd64"}}); err == nil {
		t.Fatal("duplicate target was accepted")
	}
}

func TestArchiveIsDeterministicAndOrdered(t *testing.T) {
	directory := t.TempDir()
	firstInput := filepath.Join(directory, "first")
	secondInput := filepath.Join(directory, "second")
	if err := os.WriteFile(firstInput, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondInput, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []archiveFile{{name: "z.txt", path: secondInput, mode: 0o644}, {name: "a", path: firstInput, mode: 0o755}}
	sourceTime := time.Unix(1_700_000_000, 0).UTC()
	firstArchive := filepath.Join(directory, "first.tar.gz")
	secondArchive := filepath.Join(directory, "second.tar.gz")
	if err := writeArchive(firstArchive, "factory", append([]archiveFile(nil), files...), sourceTime); err != nil {
		t.Fatal(err)
	}
	if err := writeArchive(secondArchive, "factory", append([]archiveFile(nil), files...), sourceTime); err != nil {
		t.Fatal(err)
	}
	firstDigest, _ := fileSHA256(firstArchive)
	secondDigest, _ := fileSHA256(secondArchive)
	if firstDigest != secondDigest {
		t.Fatalf("archive digests differ: %s != %s", firstDigest, secondDigest)
	}
	file, err := os.Open(firstArchive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for index, want := range []string{"factory/a", "factory/z.txt"} {
		header, err := tarReader.Next()
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != want || !header.ModTime.Equal(sourceTime) {
			t.Fatalf("entry %d = %q at %s", index, header.Name, header.ModTime)
		}
	}
}

func TestNoticesIncludeGoRuntimeAndStandardLibraryLicense(t *testing.T) {
	const license = "Copyright 2009 The Go Authors. All rights reserved.\n"
	body, err := renderNotices("go1.25.12", []byte(license), nil, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Go toolchain go1.25.12 runtime and standard library", license} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("third-party notices do not contain %q", want)
		}
	}
}

func TestSPDXIncludesArtifactAndDependency(t *testing.T) {
	body, err := renderSPDX(
		"v1.2.3", strings.Repeat("a", 40), time.Unix(1_700_000_000, 0).UTC(),
		"factory_1.2.3_linux_amd64.tar.gz", strings.Repeat("b", 64),
		[]module{{Path: "example.com/dependency", Version: "v1.0.0"}},
		[]npmPackage{{Name: "react", Version: "19.2.8", License: "MIT", Resolved: "https://registry.npmjs.org/react/-/react-19.2.8.tgz"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var document spdxDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if document.SPDXVersion != "SPDX-2.3" || len(document.Packages) != 3 || len(document.Relationships) != 3 {
		t.Fatalf("SPDX document = %#v", document)
	}
	document.Packages[0].VersionInfo = "v9.9.9"
	tampered, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	tampered = append(tampered, '\n')
	if err := verifySPDXDocument(tampered, body); err == nil {
		t.Fatal("SBOM with a forged Factory version was accepted")
	}
}

func TestSPDXModelsVersionedGoReplacement(t *testing.T) {
	body, err := renderSPDX(
		"v1.2.3", strings.Repeat("a", 40), time.Unix(1_700_000_000, 0).UTC(),
		"factory_1.2.3_linux_amd64.tar.gz", strings.Repeat("b", 64),
		[]module{{
			Path: "example.com/original", Version: "v1.0.0",
			Replace: &module{Path: "example.com/fork", Version: "v1.1.0", Dir: "/module-cache/fork"},
		}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var document spdxDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	for _, want := range []string{
		`"name": "example.com/original"`, `pkg:golang/example.com/original@v1.0.0`,
		`"name": "example.com/fork"`, `pkg:golang/example.com/fork@v1.1.0`,
		`"relationshipType": "VARIANT_OF"`,
	} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("replacement SBOM does not contain %q", want)
		}
	}
	if len(document.Packages) != 3 || len(document.Relationships) != 3 {
		t.Fatalf("replacement SPDX document = %#v", document)
	}
	if err := validateModuleReplacements([]module{{Path: "example.com/original", Replace: &module{Path: "../local"}}}); err == nil {
		t.Fatal("local replacement was accepted")
	}
}

func TestDownloadLockedModulesRejectsMissingChecksumWithoutChangingSource(t *testing.T) {
	root := t.TempDir()
	moduleFile := []byte("module example.com/release\n\ngo 1.25\n")
	sumFile := []byte("example.com/dependency v1.0.0/go.mod h1:committed\n")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), moduleFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.sum"), sumFile, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := func(_ context.Context, _ string, _ []string, _ string, arguments ...string) error {
		for _, argument := range arguments {
			const prefix = "-modfile="
			if strings.HasPrefix(argument, prefix) {
				path := strings.TrimPrefix(argument, prefix)
				resolvedSum := strings.TrimSuffix(path, ".mod") + ".sum"
				return os.WriteFile(resolvedSum, append(sumFile, []byte("example.com/dependency v1.0.0 h1:resolved\n")...), 0o600)
			}
		}
		return nil
	}
	if err := downloadLockedModules(context.Background(), root, runner); err == nil || !strings.Contains(err.Error(), "does not fully lock") {
		t.Fatalf("incomplete module lock error = %v", err)
	}
	for _, file := range []struct {
		name string
		want []byte
	}{{"go.mod", moduleFile}, {"go.sum", sumFile}} {
		body, err := os.ReadFile(filepath.Join(root, file.name))
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != string(file.want) {
			t.Fatalf("%s changed to %q", file.name, body)
		}
	}
}

func TestProductionNPMInventoryIsComplete(t *testing.T) {
	packages, err := listNPMPackages(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 6 {
		t.Fatalf("production npm package count = %d, want 6", len(packages))
	}
	names := make([]string, len(packages))
	for index, item := range packages {
		names[index] = item.Name
	}
	for _, want := range []string{"@tanstack/query-core", "@tanstack/react-query", "lucide-react", "react", "react-dom", "scheduler"} {
		if !contains(names, want) {
			t.Fatalf("production npm inventory is missing %s: %v", want, names)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestVerifyRejectsUnchecksummedFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "artifact"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeChecksums(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "extra"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksums(directory); err == nil || !strings.Contains(err.Error(), "not covered") {
		t.Fatalf("extra file error = %v", err)
	}
}

func TestVerifyRejectsEmptyRelease(t *testing.T) {
	err := Verify(context.Background(), filepath.Join("..", ".."), t.TempDir(), "v1.2.3", strings.Repeat("a", 40), Target{OS: "linux", Arch: "amd64"}, true)
	if err == nil {
		t.Fatal("empty release was accepted")
	}
}
