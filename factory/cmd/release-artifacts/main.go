package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/owainlewis/factory/internal/releasebuilder"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "release-artifacts:", err)
		os.Exit(1)
	}
}

func run() error {
	root := flag.String("root", ".", "repository root")
	output := flag.String("output", "dist", "release artifact directory")
	version := flag.String("version", "", "release version such as v1.2.3")
	commit := flag.String("commit", "", "full source commit")
	verifyTarget := flag.String("verify-target", "", "verify one target such as linux/amd64")
	execute := flag.Bool("execute", false, "execute verified binaries on the native target")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	absoluteOutput := *output
	if !filepath.IsAbs(absoluteOutput) {
		absoluteOutput = filepath.Join(absoluteRoot, absoluteOutput)
	}
	if *verifyTarget != "" {
		target, err := parseTarget(*verifyTarget)
		if err != nil {
			return err
		}
		return releasebuilder.Verify(context.Background(), absoluteRoot, absoluteOutput, *version, *commit, target, *execute)
	}
	if *execute {
		return fmt.Errorf("-execute requires -verify-target")
	}
	return releasebuilder.Build(context.Background(), releasebuilder.Options{
		Root: absoluteRoot, Output: absoluteOutput, Version: *version, Commit: *commit,
		Stdout: os.Stdout,
	})
}

func parseTarget(value string) (releasebuilder.Target, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return releasebuilder.Target{}, fmt.Errorf("target must look like linux/amd64")
	}
	for _, target := range releasebuilder.DefaultTargets {
		if target.OS == parts[0] && target.Arch == parts[1] {
			return target, nil
		}
	}
	return releasebuilder.Target{}, fmt.Errorf("unsupported release target %q", value)
}
