package main

import "testing"

func TestParseTarget(t *testing.T) {
	target, err := parseTarget("darwin/arm64")
	if err != nil {
		t.Fatal(err)
	}
	if target.OS != "darwin" || target.Arch != "arm64" {
		t.Fatalf("target = %#v", target)
	}
	for _, value := range []string{"", "linux", "windows/amd64", "linux/386", "linux/amd64/extra"} {
		if _, err := parseTarget(value); err == nil {
			t.Fatalf("target %q was accepted", value)
		}
	}
}
