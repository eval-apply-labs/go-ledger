//go:build unix && !solaris && !aix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// lockFolder opens the folder at path and takes an exclusive lock on it (flock), held until the folder
// is closed: while the signer runs. A second signer whose sign log is in the folder is refused, in
// another network namespace too, as a second container on the same volume is. The lock is the folder's,
// not the sign log's, because a rewrite renames a new file over the log.
func lockFolder(path string) (*os.File, error) {
	folder, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("the sign log's folder: %w", err)
	}
	if err := syscall.Flock(int(folder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		folder.Close()
		return nil, fmt.Errorf("the sign log's folder %s is locked by a signer running on it, such as this one: "+
			"stop that one first, or give each signer a folder of its own (%v)", path, err)
	}
	return folder, nil
}
