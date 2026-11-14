//go:build linux && cgo

package fs

import (
	"errors"
	"strings"
)

// Errors returned by the probing functions.
var (
	// ErrNoFilesystem is returned when a probe ran to completion without
	// recognizing any filesystem superblock. That is the result for an empty
	// or zeroed region, for data that holds no filesystem, for a filesystem
	// libblkid does not know, and for one whose superblock lies beyond the end
	// of the buffer.
	ErrNoFilesystem = errors.New("memblkid: no filesystem recognized in buffer")

	// ErrNoBlockSize is returned when a filesystem was recognized in the
	// buffer but did not report a block size. libblkid reports BLOCK_SIZE for
	// most, but not all, of the filesystems it handles.
	ErrNoBlockSize = errors.New("memblkid: recognized filesystem reports no block size")
)

// probeOptions is the resolved form of the ProbeOption values given to a
// probing function. Options only ever add to it; probing starts once every
// option has been applied.
type probeOptions struct {
	// onlyFilesystems restricts probing to these filesystem names. Empty means
	// "try everything libblkid knows".
	onlyFilesystems []string
}

// ProbeOption configures a single probe. Options are applied in the order they
// are given, before any probing happens, and an option that fails aborts the
// probe with that error.
type ProbeOption func(*probeOptions) error

// WithFilesystemFilter restricts probing to the named filesystems, for example
// WithFilesystemFilter("ext4", "xfs"), which is faster and avoids a
// mis-detection when the caller already knows what to expect.
//
// Names are the ones libblkid supports; SupportedFilesystems reports them. A
// name libblkid does not know is not an error, it simply never matches, but an
// empty name, and a call with no names at all, are rejected.
func WithFilesystemFilter(filesystems ...string) ProbeOption {
	return func(options *probeOptions) error {
		if len(filesystems) == 0 {
			return errors.New("memblkid: WithFilesystemFilter: no filesystems given")
		}
		for _, filesystem := range filesystems {
			if strings.TrimSpace(filesystem) == "" {
				return errors.New("memblkid: WithFilesystemFilter: empty filesystem name")
			}
		}
		options.onlyFilesystems = append(options.onlyFilesystems, filesystems...)
		return nil
	}
}
