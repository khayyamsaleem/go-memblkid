//go:build linux && cgo

// Package fs identifies the filesystem held in a block of memory.
//
// It answers the question "what is in these bytes?" without touching the device
// they came from: callers read the head of a device, image or archive member
// into a buffer and hand that over. Detection is done by libblkid
// (util-linux), the library behind blkid(8) and lsblk(8), which recognizes and
// disambiguates everything from ext2 to btrfs, XFS, vfat, NTFS and LVM2_member.
//
// # Platform
//
// Probing needs libblkid and cgo, so this package builds on Linux with
// CGO_ENABLED=1 only. Guard imports of it with a "//go:build linux" constraint
// in code that must also build elsewhere.
//
// Building requires pkg-config and the libblkid development files:
//
//	apt-get install libblkid-dev     # Debian/Ubuntu
//	dnf install libblkid-devel       # Fedora/RHEL
//	zypper install libblkid-devel    # openSUSE
//	pacman -S util-linux             # Arch/Manjaro
//
// # Buffers
//
// A buffer is a read-only sample of the start of the filesystem, and its size
// decides what can be found. libblkid's rules are specific:
//
//   - areas of 1 KiB or less are ignored completely;
//   - a prober is skipped when the area is smaller than the minimum it
//     declares, and btrfs declares 1 MiB;
//   - RAID-usage probers are skipped in "tiny" areas of 1440 KiB or less, which
//     is why LVM2_member needs a bigger sample than its label at offset 1 KiB
//     would suggest;
//   - some filesystems are only accepted once libblkid has validated structures
//     deeper in the volume (it reads an HFS+ catalog B-tree, for instance), so a
//     head sample may not identify them at all.
//
// About 2 KiB covers ext2, ext3, ext4, XFS and vfat, and 2 MiB covers everything
// else except filesystems that need structures further in. A sample that is too
// small is not an error: the probe simply reports ErrNoFilesystem.
//
// # Concurrency
//
// Every function is safe for concurrent use by multiple goroutines. Each probe
// works on its own copy of the buffer and its own libblkid state; nothing is
// shared or cached between calls.
//
// # Ownership
//
// Probing copies the buffer into a private anonymous in-memory file (memfd);
// the caller keeps ownership of the buffer and may reuse or release it as soon
// as a call returns. Nothing passed in is retained, and every resource the
// package allocates is released before the call returns.
//
// # Example
//
//	f, err := os.Open("/dev/nvme0n1")
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer f.Close()
//
//	head := make([]byte, 64*1024)
//	if _, err := io.ReadFull(f, head); err != nil {
//		log.Fatal(err)
//	}
//
//	fstype, err := fs.GetFilesystemTypeFromBuffer(head)
//	if err != nil {
//		log.Fatal(err)
//	}
//	fmt.Println(fstype) // ext4
//
// See the example functions in this package for runnable versions.
package fs
