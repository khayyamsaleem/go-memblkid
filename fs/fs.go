//go:build linux && cgo

package fs

/*
#cgo pkg-config: blkid
#include <stdlib.h>

#include "memblkid.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"sort"
	"syscall"
	"unsafe"
)

// GetFilesystemTypeFromBuffer reports the filesystem type of the superblock
// found in data, for example "ext4", "xfs", "vfat" or "LVM2_member".
//
// data is a read-only sample of the start of a device, image or archive member;
// see the package documentation for what its size makes detectable. data is not
// modified and is not referenced after the call returns.
//
// It returns ErrNoFilesystem when no supported superblock was recognized.
func GetFilesystemTypeFromBuffer(data []byte, options ...ProbeOption) (string, error) {
	info, err := probe(data, options...)
	if err != nil {
		return "", err
	}
	return info.filesystem, nil
}

// GetBlockSizeFromBuffer reports the allocation unit of the filesystem found in
// data, in bytes: 4096 for a typical ext4 or XFS filesystem, the cluster size
// for vfat.
//
// The value comes from libblkid's FSBLOCKSIZE where the filesystem reports one,
// falling back to BLOCK_SIZE, which for some filesystems (XFS and vfat among
// them) is the sector size rather than the block size. Its arguments and errors
// are those of GetFilesystemTypeFromBuffer, except that it returns
// ErrNoBlockSize when the recognized filesystem reports neither.
func GetBlockSizeFromBuffer(data []byte, options ...ProbeOption) (uint64, error) {
	info, err := probe(data, options...)
	if err != nil {
		return 0, err
	}
	if info.blockSize == 0 {
		return 0, fmt.Errorf("%w: %s", ErrNoBlockSize, info.filesystem)
	}
	return info.blockSize, nil
}

// SupportedFilesystems returns the name of every filesystem libblkid is able to
// recognize, sorted, such as "ext4", "xfs" or "vfat". The names are the ones
// accepted by WithFilesystemFilter. It returns nil if the list cannot be
// obtained.
func SupportedFilesystems() []string {
	var count C.size_t

	list := C.memblkid_list_supported_filesystems(&count)
	if list == nil {
		return nil
	}
	defer C.memblkid_free_supported_filesystems(list)

	names := unsafe.Slice(list, int(count))
	filesystems := make([]string, 0, int(count))
	for _, name := range names {
		filesystems = append(filesystems, C.GoString(name))
	}
	// libblkid returns the names in probing order, which is an implementation
	// detail; sorting keeps the result stable for callers and across versions.
	sort.Strings(filesystems)
	return filesystems
}

// probeResult is what a successful probe found.
type probeResult struct {
	filesystem string
	blockSize  uint64
}

// probe copies data into a probe and asks libblkid what it holds.
func probe(data []byte, options ...ProbeOption) (probeResult, error) {
	var opts probeOptions
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&opts); err != nil {
			return probeResult{}, err
		}
	}

	if len(data) == 0 {
		return probeResult{}, errors.New("memblkid: buffer is empty")
	}

	// Only the length of data is taken here; the pointer is read by C during
	// the call and never kept, so the cgo pointer rules are satisfied.
	mp, err := newProbe(data)
	if err != nil {
		return probeResult{}, err
	}
	defer C.memblkid_free_probe(&mp) // frees the probe and closes its file descriptor

	if len(opts.onlyFilesystems) > 0 {
		if err := setFilesystemFilter(mp.pr, opts.onlyFilesystems); err != nil {
			return probeResult{}, err
		}
	}

	var info C.struct_memblkid_info
	switch rc := C.memblkid_probe_info(mp.pr, &info); rc {
	case 0:
	case 1:
		return probeResult{}, ErrNoFilesystem
	default:
		return probeResult{}, errors.New("memblkid: probing buffer failed")
	}

	return probeResult{
		filesystem: C.GoString(&info.fstype[0]),
		blockSize:  uint64(info.block_size),
	}, nil
}

// newProbe hands data to libblkid through a private in-memory file. The
// returned probe owns that file descriptor; see memblkid_free_probe().
func newProbe(data []byte) (C.struct_memblkid_probe, error) {
	var cErr C.int

	mp := C.memblkid_probe_from_buffer(
		(*C.uchar)(unsafe.Pointer(&data[0])),
		C.size_t(len(data)),
		&cErr,
	)
	if mp.pr == nil {
		return mp, fmt.Errorf("memblkid: create probe from buffer: %w", syscall.Errno(cErr))
	}
	return mp, nil
}

// setFilesystemFilter restricts the probe to the named filesystems. libblkid
// copies the names into a per-probe bitmap, so they can be released as soon as
// the call returns.
func setFilesystemFilter(pr C.blkid_probe, filesystems []string) error {
	cNames := make([]*C.char, len(filesystems))
	for i, filesystem := range filesystems {
		cNames[i] = C.CString(filesystem)
	}
	defer func() {
		for _, name := range cNames {
			C.free(unsafe.Pointer(name))
		}
	}()

	if rc := C.blkid_probe_filter_superblocks_type(pr, C.BLKID_FLTR_ONLYIN, &cNames[0]); rc != 0 {
		return errors.New("memblkid: setting filesystem filter failed")
	}
	return nil
}
