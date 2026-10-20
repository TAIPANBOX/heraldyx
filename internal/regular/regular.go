// Package regular opens a stream file only when it is a regular file.
package regular

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrNotRegular is what Open returns for a path that is there and is
// not a regular file.
var ErrNotRegular = errors.New("not a regular file")

// Open opens a stream for reading only when it is a regular file
// (invariant 33). O_NOFOLLOW refuses a link at the path, and O_NONBLOCK makes
// the open of a FIFO return at once instead of waiting for a writer, which
// would stop the one poll loop and every alert behind it. Every open of an
// event stream goes through here: the watcher's and the checkpoint gate's. The file's type is
// then taken from the open descriptor, not from a stat of the path, so a swap
// between a check and the open cannot change what is read. O_NONBLOCK does
// not change how a regular file reads.
func Open(path string) (*os.File, error) {
	// #nosec G304 -- the paths are operator-supplied configuration.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, ErrNotRegular)
	}
	return f, nil
}
