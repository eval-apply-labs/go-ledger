// Command renametool supports a behaviour-preserving, codebase-wide rename of the go-ledger
// repository. It has three subcommands:
//
//	renametool inventory [-o inventory.json] <tree>
//	    Lists every object declared in the tree's modules, one JSON record each, and prints
//	    counts by package and kind to stderr.
//
//	renametool apply [-comments stale.txt] <tree> <renames.json>
//	    Validates a rename map, rewrites <tree> in place, and refuses (changing nothing) when
//	    the map is invalid or when the rewritten tree is not provably the same program.
//
//	renametool check [-table renames.md] <oldTree> <newTree>
//	    Proves that <newTree> is <oldTree> with only names changed, or lists every violation
//	    and exits with status 1.
//
// The tool uses only the standard library. It type-checks the trees with the go/types package
// compiled into it and the standard-library sources of the GOROOT it was built with, so it must be
// built by the toolchain the trees use (go1.27.3): run it as `GOTOOLCHAIN=auto go run . <args>`
// from this directory, or build it the same way. verifyToolchain refuses any mismatch.
package main

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	if err := verifyToolchain(); err != nil {
		fmt.Fprintln(os.Stderr, "renametool:", err)
		os.Exit(2)
	}
	var status int
	switch os.Args[1] {
	case "inventory":
		status = runInventory(os.Args[2:])
	case "apply":
		status = runApply(os.Args[2:])
	case "check":
		status = runCheck(os.Args[2:])
	default:
		usage()
	}
	os.Exit(status)
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  renametool inventory [-o inventory.json] <tree>
  renametool apply [-comments stale.txt] <tree> <renames.json>
  renametool check [-table renames.md] <oldTree> <newTree>
`)
	os.Exit(2)
}

// verifyToolchain makes sure the standard-library sources the tool reads (GOROOT) belong to the
// same Go release as the go/types code compiled into the tool. A mismatch would type-check the
// trees against a different standard library than the one `go build` uses.
func verifyToolchain() error {
	goroot := build.Default.GOROOT
	data, err := os.ReadFile(filepath.Join(goroot, "VERSION"))
	if err != nil {
		return fmt.Errorf("cannot read the GOROOT version file: %v", err)
	}
	version, _, _ := strings.Cut(string(data), "\n")
	version = strings.TrimSpace(version)
	if version != runtime.Version() {
		return fmt.Errorf("the tool was built with %s but GOROOT %s holds %s; build and run it with "+
			"GOTOOLCHAIN=auto from its own directory, and leave GOROOT unset", runtime.Version(), goroot, version)
	}
	return nil
}

// fatalf prints an error and exits with status 2 (a usage or environment problem, as opposed to
// status 1, which means "refused" for apply and "violations found" for check).
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "renametool: "+format+"\n", args...)
	os.Exit(2)
}
