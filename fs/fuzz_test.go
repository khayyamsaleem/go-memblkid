//go:build linux && cgo

package fs

import (
	"bytes"
	"errors"
	"testing"
)

// Fuzzing hunts for inputs that crash the C library or make the Go layer
// misbehave: arbitrary contents, arbitrary sizes, and sizes too small to hold
// any superblock at all. The seeds run as part of an ordinary go test.
func FuzzGetFilesystemTypeFromBuffer(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 1024))
	f.Add(make([]byte, 1536))
	f.Add(extBuffer(headSize, 0, 0, 0))
	f.Add(extBuffer(headSize, 2, 0, 0))
	f.Add(extBuffer(headSize, 2, extHasJournal, 0))
	f.Add(extBuffer(headSize, 2, extHasJournal, extExtents))
	f.Add(randomBytes(headSize))

	f.Fuzz(func(t *testing.T, data []byte) {
		before := bytes.Clone(data)

		fsType, err := GetFilesystemTypeFromBuffer(data)
		if !bytes.Equal(before, data) {
			t.Fatal("probing modified the caller's buffer")
		}
		if err != nil {
			if !errors.Is(err, ErrNoFilesystem) && fsType != "" {
				t.Errorf("error %v came with filesystem type %q", err, fsType)
			}
			return
		}
		if fsType == "" {
			t.Error("no error but no filesystem type")
		}

		// The same bytes must lead to the same answer, so a filesystem that
		// was recognized either reports a block size or says it has none.
		blockSize, err := GetBlockSizeFromBuffer(data)
		switch {
		case errors.Is(err, ErrNoBlockSize):
		case err != nil:
			t.Errorf("GetBlockSizeFromBuffer() = %d, %v for a recognized %s", blockSize, err, fsType)
		case blockSize == 0:
			t.Error("block size 0 without ErrNoBlockSize")
		}
	})
}
