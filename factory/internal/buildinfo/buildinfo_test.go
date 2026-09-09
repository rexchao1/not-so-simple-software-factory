package buildinfo

import "testing"

func TestVersionCommandsAndString(t *testing.T) {
	for _, arguments := range [][]string{{"version"}, {"--version"}} {
		if !Requested(arguments) {
			t.Fatalf("%v was not recognized", arguments)
		}
	}
	for _, arguments := range [][]string{nil, {"version", "extra"}, {"-version"}} {
		if Requested(arguments) {
			t.Fatalf("%v was recognized", arguments)
		}
	}
	oldVersion, oldCommit := Version, Commit
	t.Cleanup(func() { Version, Commit = oldVersion, oldCommit })
	Version, Commit = "v1.2.3", "0123456789abcdef"
	if got := String("factory-server"); got != "factory-server v1.2.3 (commit 0123456789abcdef)" {
		t.Fatalf("version string = %q", got)
	}
}
