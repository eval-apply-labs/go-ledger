//go:build !unix || solaris || aix

package main

import "os"

// lockFolder opens the folder at path, and locks nothing: there is no flock here (lock_unix.go). The
// signer runs on Linux; this keeps it building elsewhere.
func lockFolder(path string) (*os.File, error) { return os.Open(path) }
