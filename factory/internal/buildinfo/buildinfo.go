package buildinfo

import "fmt"

// Version and Commit are replaced by the release build using -ldflags -X.
var (
	Version = "dev"
	Commit  = "unknown"
)

func String(binary string) string {
	return fmt.Sprintf("%s %s (commit %s)", binary, Version, Commit)
}

func Requested(arguments []string) bool {
	return len(arguments) == 1 && (arguments[0] == "version" || arguments[0] == "--version")
}
