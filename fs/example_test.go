//go:build linux && cgo

package fs_test

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/khayyamsaleem/go-memblkid/fs"
)

// ext2Sample returns a buffer holding the start of an ext2 filesystem. Only the
// superblock magic has to be in place for it to be recognized.
func ext2Sample() []byte {
	const (
		superblockOffset = 1024
		magicOffset      = superblockOffset + 0x38 // s_magic = 0xEF53
	)

	sample := make([]byte, 64*1024)
	binary.LittleEndian.PutUint16(sample[magicOffset:], 0xEF53)
	return sample
}

func ExampleGetFilesystemTypeFromBuffer() {
	fsType, err := fs.GetFilesystemTypeFromBuffer(ext2Sample())
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(fsType)

	// Output: ext2
}

func ExampleGetBlockSizeFromBuffer() {
	sample := ext2Sample()
	// s_log_block_size sits at 0x18 of the superblock; 2 means 1024<<2 bytes.
	sample[1024+0x18] = 2

	blockSize, err := fs.GetBlockSizeFromBuffer(sample)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(blockSize)

	// Output: 4096
}

func ExampleWithFilesystemFilter() {
	sample := ext2Sample()

	// The sample holds an ext2 filesystem, so restricting the probe to xfs
	// finds nothing, while naming ext2 or xfs does.
	_, err := fs.GetFilesystemTypeFromBuffer(sample, fs.WithFilesystemFilter("xfs"))
	fmt.Println(errors.Is(err, fs.ErrNoFilesystem))

	fsType, _ := fs.GetFilesystemTypeFromBuffer(sample, fs.WithFilesystemFilter("ext2", "xfs"))
	fmt.Println(fsType)

	// Output:
	// true
	// ext2
}
