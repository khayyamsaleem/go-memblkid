// Command blkidscan prints the filesystem type and block size of block devices.
//
// With no arguments it scans the block devices directly under /dev, which
// usually needs root; with arguments it probes exactly those devices or image
// files.
//
//	blkidscan /dev/nvme0n1
//	sudo blkidscan
//
// It is also a small demonstration of the go-memblkid module; the probing in
// fs is what does the work.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/khayyamsaleem/go-memblkid/fs"
)

// sampleSize is how much of a device is read before probing. 2 MiB is enough
// for the filesystems libblkid will only probe above a minimum size: btrfs
// needs 1 MiB, and RAID-usage types such as LVM2_member are skipped in "tiny"
// areas of 1440 KiB or less.
const sampleSize = 2 * 1024 * 1024

func main() {
	devDir := flag.String("dev", "/dev", "directory to scan for block devices when no arguments are given")
	flag.Parse()

	targets := flag.Args()
	if len(targets) == 0 {
		var err error
		if targets, err = blockDevices(*devDir); err != nil {
			fmt.Fprintln(os.Stderr, "blkidscan:", err)
			os.Exit(1)
		}
		if len(targets) == 0 {
			fmt.Fprintln(os.Stderr, "blkidscan: no block devices found in", *devDir)
			os.Exit(1)
		}
	}

	failed := false
	for _, target := range targets {
		if err := scan(target); err != nil {
			fmt.Fprintf(os.Stderr, "blkidscan: %s: %v\n", target, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func scan(path string) error {
	sample, err := readSample(path)
	if err != nil {
		return err
	}

	fsType, err := fs.GetFilesystemTypeFromBuffer(sample)
	if err != nil {
		if errors.Is(err, fs.ErrNoFilesystem) {
			fmt.Printf("%s: no filesystem recognized in the first %d bytes\n", path, len(sample))
			return nil
		}
		return err
	}

	blockSize, err := fs.GetBlockSizeFromBuffer(sample)
	if err != nil {
		fmt.Printf("%s: type=%s block_size=unknown\n", path, fsType)
		return nil
	}
	fmt.Printf("%s: type=%s block_size=%d\n", path, fsType, blockSize)
	return nil
}

func readSample(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	sample := make([]byte, sampleSize)
	n, err := io.ReadFull(file, sample)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return sample[:n], nil
}

// blockDevices lists the block devices directly under dir.
func blockDevices(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var devices []string
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
			continue
		}
		devices = append(devices, filepath.Join(dir, entry.Name()))
	}
	return devices, nil
}
