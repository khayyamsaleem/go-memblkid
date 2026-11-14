//go:build linux && cgo

package fs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"
)

// Field offsets and feature flags of the ext-series superblock, which starts
// 1024 bytes into the filesystem. See the ext4 on-disk layout, and probe_ext2,
// probe_ext3 and probe_ext4 in libblkid's superblocks/ext.c.
const (
	extSuperblockOffset = 1024
	extLogBlockSize     = extSuperblockOffset + 0x18 // s_log_block_size
	extMagic            = extSuperblockOffset + 0x38 // s_magic, 0xEF53
	extFeatureCompat    = extSuperblockOffset + 0x5C // s_feature_compat
	extFeatureIncompat  = extSuperblockOffset + 0x60 // s_feature_incompat

	extHasJournal = 0x0004 // EXT3_FEATURE_COMPAT_HAS_JOURNAL
	extExtents    = 0x0040 // EXT4_FEATURE_INCOMPAT_EXTENTS
)

// headSize is the sample size the package documentation recommends.
const headSize = 64 * 1024

// extBuffer returns a buffer shaped like the head of an ext-series filesystem:
// an ext2 filesystem when neither feature flag is set, ext3 when the journal
// flag is, and ext4 when both are. logBlockSize is s_log_block_size, where a
// value of n means a block size of 1024<<n.
func extBuffer(size int, logBlockSize byte, compat, incompat uint32) []byte {
	buffer := make([]byte, size)
	binary.LittleEndian.PutUint16(buffer[extMagic:], 0xEF53)
	binary.LittleEndian.PutUint32(buffer[extLogBlockSize:], uint32(logBlockSize))
	binary.LittleEndian.PutUint32(buffer[extFeatureCompat:], compat)
	binary.LittleEndian.PutUint32(buffer[extFeatureIncompat:], incompat)
	return buffer
}

func TestGetFilesystemTypeFromBuffer(t *testing.T) {
	tests := []struct {
		name   string
		buffer []byte
		want   string
	}{
		{"ext2", extBuffer(headSize, 2, 0, 0), "ext2"},
		{"ext3", extBuffer(headSize, 2, extHasJournal, 0), "ext3"},
		{"ext4", extBuffer(headSize, 2, extHasJournal, extExtents), "ext4"},
		{"ext2 in a 4 KiB sample", extBuffer(4*1024, 2, 0, 0), "ext2"},
		{"ext2 with a 64 KiB sample", extBuffer(64*1024*1024, 2, 0, 0), "ext2"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := GetFilesystemTypeFromBuffer(test.buffer)
			if err != nil {
				t.Fatalf("GetFilesystemTypeFromBuffer() error = %v", err)
			}
			if got != test.want {
				t.Errorf("GetFilesystemTypeFromBuffer() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGetBlockSizeFromBuffer(t *testing.T) {
	tests := []struct {
		name         string
		logBlockSize byte
		want         uint64
	}{
		{"1 KiB blocks", 0, 1024},
		{"2 KiB blocks", 1, 2048},
		{"4 KiB blocks", 2, 4096},
		{"8 KiB blocks", 3, 8192},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := GetBlockSizeFromBuffer(extBuffer(headSize, test.logBlockSize, 0, 0))
			if err != nil {
				t.Fatalf("GetBlockSizeFromBuffer() error = %v", err)
			}
			if got != test.want {
				t.Errorf("GetBlockSizeFromBuffer() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestGetFilesystemTypeFromBuffer_NoFilesystem(t *testing.T) {
	tests := []struct {
		name   string
		buffer []byte
	}{
		{"zeroed", make([]byte, headSize)},
		{"unrecognizable", randomBytes(headSize)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fsType, err := GetFilesystemTypeFromBuffer(test.buffer)
			if !errors.Is(err, ErrNoFilesystem) {
				t.Fatalf("GetFilesystemTypeFromBuffer() error = %v, want %v", err, ErrNoFilesystem)
			}
			if fsType != "" {
				t.Errorf("GetFilesystemTypeFromBuffer() = %q, want no type", fsType)
			}

			if _, err := GetBlockSizeFromBuffer(test.buffer); !errors.Is(err, ErrNoFilesystem) {
				t.Errorf("GetBlockSizeFromBuffer() error = %v, want %v", err, ErrNoFilesystem)
			}
		})
	}
}

// An empty buffer must be rejected rather than crash or be probed.
func TestGetFilesystemTypeFromBuffer_EmptyBuffer(t *testing.T) {
	for _, buffer := range [][]byte{nil, {}} {
		if _, err := GetFilesystemTypeFromBuffer(buffer); err == nil {
			t.Fatalf("GetFilesystemTypeFromBuffer(%#v) error = nil, want an error", buffer)
		} else if errors.Is(err, ErrNoFilesystem) {
			t.Fatalf("GetFilesystemTypeFromBuffer(%#v) error = %v, want an input error", buffer, err)
		}
		if _, err := GetBlockSizeFromBuffer(buffer); err == nil {
			t.Fatalf("GetBlockSizeFromBuffer(%#v) error = nil, want an error", buffer)
		}
	}
}

func TestWithFilesystemFilter(t *testing.T) {
	ext2 := extBuffer(headSize, 2, 0, 0)

	tests := []struct {
		name        string
		options     []ProbeOption
		want        string
		wantErr     error
		wantErrText string
	}{
		{name: "matching filesystem", options: []ProbeOption{WithFilesystemFilter("ext2")}, want: "ext2"},
		{name: "matching filesystem among several", options: []ProbeOption{WithFilesystemFilter("xfs", "ext2")}, want: "ext2"},
		{name: "duplicated names", options: []ProbeOption{WithFilesystemFilter("ext2", "ext2")}, want: "ext2"},
		{name: "filesystem filtered out", options: []ProbeOption{WithFilesystemFilter("xfs")}, wantErr: ErrNoFilesystem},
		{name: "sibling filesystem filtered out", options: []ProbeOption{WithFilesystemFilter("ext4")}, wantErr: ErrNoFilesystem},
		{name: "unknown filesystem", options: []ProbeOption{WithFilesystemFilter("nosuchfs")}, wantErr: ErrNoFilesystem},
		{name: "no names", options: []ProbeOption{WithFilesystemFilter()}, wantErrText: "no filesystems given"},
		{name: "empty name", options: []ProbeOption{WithFilesystemFilter("")}, wantErrText: "empty filesystem name"},
		{name: "blank name", options: []ProbeOption{WithFilesystemFilter("  ")}, wantErrText: "empty filesystem name"},
		{name: "ignored nil option", options: []ProbeOption{nil, WithFilesystemFilter("ext2")}, want: "ext2"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := GetFilesystemTypeFromBuffer(ext2, test.options...)
			switch {
			case test.wantErr != nil:
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("GetFilesystemTypeFromBuffer() error = %v, want %v", err, test.wantErr)
				}
			case test.wantErrText != "":
				if err == nil || !bytes.Contains([]byte(err.Error()), []byte(test.wantErrText)) {
					t.Fatalf("GetFilesystemTypeFromBuffer() error = %v, want it to contain %q", err, test.wantErrText)
				}
			default:
				if err != nil {
					t.Fatalf("GetFilesystemTypeFromBuffer() error = %v", err)
				}
				if got != test.want {
					t.Errorf("GetFilesystemTypeFromBuffer() = %q, want %q", got, test.want)
				}
			}
		})
	}
}

// An option that fails aborts the probe before any probing happens.
func TestProbeOptionError(t *testing.T) {
	wantErr := errors.New("option refuses to run")
	failing := ProbeOption(func(*probeOptions) error { return wantErr })

	for _, probe := range []struct {
		name string
		call func([]byte, ...ProbeOption) error
	}{
		{"GetFilesystemTypeFromBuffer", func(data []byte, options ...ProbeOption) error {
			_, err := GetFilesystemTypeFromBuffer(data, options...)
			return err
		}},
		{"GetBlockSizeFromBuffer", func(data []byte, options ...ProbeOption) error {
			_, err := GetBlockSizeFromBuffer(data, options...)
			return err
		}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			// Errors from options are reported even for a buffer that would
			// otherwise fail to be an empty one.
			if err := probe.call(nil, failing); !errors.Is(err, wantErr) {
				t.Errorf("call() error = %v, want %v", err, wantErr)
			}
		})
	}
}

// Probing reads the caller's buffer but must never write to it.
func TestProbeDoesNotModifyBuffer(t *testing.T) {
	buffer := extBuffer(headSize, 2, extHasJournal, extExtents)
	before := slices.Clone(buffer)

	if _, err := GetFilesystemTypeFromBuffer(buffer); err != nil {
		t.Fatalf("GetFilesystemTypeFromBuffer() error = %v", err)
	}
	if _, err := GetBlockSizeFromBuffer(buffer, WithFilesystemFilter("ext4")); err != nil {
		t.Fatalf("GetBlockSizeFromBuffer() error = %v", err)
	}

	if !bytes.Equal(before, buffer) {
		t.Errorf("buffer was modified by probing")
	}
}

func TestSupportedFilesystems(t *testing.T) {
	got := SupportedFilesystems()
	if len(got) == 0 {
		t.Fatal("SupportedFilesystems() = empty, want the filesystems libblkid knows")
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("SupportedFilesystems() = %v, want a sorted list", got)
	}

	// A buffer holding an ext2 superblock was recognized as ext2 above, so
	// these names must be offered; the rest depend on how libblkid was built.
	for _, want := range []string{"ext2", "ext3", "ext4", "xfs", "vfat"} {
		if !slices.Contains(got, want) {
			t.Errorf("SupportedFilesystems() = %v, want it to contain %q", got, want)
		}
	}

	if second := SupportedFilesystems(); !slices.Equal(got, second) {
		t.Errorf("SupportedFilesystems() = %v on the second call, want %v", second, got)
	}
}

func TestConcurrentProbing(t *testing.T) {
	tests := []struct {
		want   string
		buffer []byte
	}{
		{"ext2", extBuffer(headSize, 2, 0, 0)},
		{"ext3", extBuffer(headSize, 2, extHasJournal, 0)},
		{"ext4", extBuffer(headSize, 2, extHasJournal, extExtents)},
	}

	var wg sync.WaitGroup
	for _, test := range tests {
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 25 {
					got, err := GetFilesystemTypeFromBuffer(test.buffer)
					if err != nil {
						t.Errorf("GetFilesystemTypeFromBuffer() error = %v", err)
						return
					}
					if got != test.want {
						t.Errorf("GetFilesystemTypeFromBuffer() = %q, want %q", got, test.want)
						return
					}
					if len(SupportedFilesystems()) == 0 {
						t.Error("SupportedFilesystems() is empty")
						return
					}
				}
			}()
		}
	}
	wg.Wait()
}

// Every probe copies the buffer into an anonymous in-memory file, so a leaked
// descriptor would show up here long before a long-running process runs out of
// them.
func TestProbingLeaksNoFileDescriptors(t *testing.T) {
	buffer := extBuffer(headSize, 2, 0, 0)

	warmUp := func() {
		for range 10 {
			if _, err := GetFilesystemTypeFromBuffer(buffer); err != nil {
				t.Fatalf("GetFilesystemTypeFromBuffer() error = %v", err)
			}
		}
	}
	warmUp()

	before := openFileDescriptors(t)
	for range 200 {
		if _, err := GetFilesystemTypeFromBuffer(buffer); err != nil {
			t.Fatalf("GetFilesystemTypeFromBuffer() error = %v", err)
		}
		if _, err := GetBlockSizeFromBuffer(buffer, WithFilesystemFilter("ext2")); err != nil {
			t.Fatalf("GetBlockSizeFromBuffer() error = %v", err)
		}
		SupportedFilesystems()
	}
	if after := openFileDescriptors(t); after > before {
		t.Errorf("open file descriptors grew from %d to %d over 600 probes", before, after)
	}
}

func TestGetFilesystemTypeFromBuffer_Images(t *testing.T) {
	tests := []struct {
		name          string
		tool          string
		args          []string
		sizeMiB       int
		headBytes     int
		want          string
		wantBlockSize uint64
	}{
		// Every image is created with an explicit geometry, so the expected
		// block size does not depend on the mkfs utility's defaults.
		{name: "ext4", tool: "mkfs.ext4", args: []string{"-q", "-F", "-b", "4096"}, sizeMiB: 64, headBytes: headSize, want: "ext4", wantBlockSize: 4096},
		{name: "ext4 with 1 KiB blocks", tool: "mkfs.ext4", args: []string{"-q", "-F", "-b", "1024"}, sizeMiB: 64, headBytes: headSize, want: "ext4", wantBlockSize: 1024},
		{name: "xfs", tool: "mkfs.xfs", args: []string{"-q", "-f", "-b", "size=4096"}, sizeMiB: 512, headBytes: headSize, want: "xfs", wantBlockSize: 4096},
		// vfat reports its cluster size: one 512-byte sector here.
		{name: "vfat", tool: "mkfs.vfat", args: []string{"-F", "32", "-s", "1", "-S", "512"}, sizeMiB: 64, headBytes: headSize, want: "vfat", wantBlockSize: 512},
		{name: "btrfs", tool: "mkfs.btrfs", args: []string{"-q", "-f", "-s", "4096"}, sizeMiB: 128, headBytes: minBtrfsSample, want: "btrfs", wantBlockSize: 4096},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			head := filesystemImageHead(t, test.tool, test.args, test.sizeMiB, test.headBytes)

			got, err := GetFilesystemTypeFromBuffer(head)
			if err != nil {
				t.Fatalf("GetFilesystemTypeFromBuffer() error = %v, want %q", err, test.want)
			}
			if got != test.want {
				t.Errorf("GetFilesystemTypeFromBuffer() = %q, want %q", got, test.want)
			}

			blockSize, err := GetBlockSizeFromBuffer(head)
			if err != nil {
				t.Fatalf("GetBlockSizeFromBuffer() error = %v, want %d", err, test.wantBlockSize)
			}
			if blockSize != test.wantBlockSize {
				t.Errorf("GetBlockSizeFromBuffer() = %d, want %d", blockSize, test.wantBlockSize)
			}
		})
	}
}

// minBtrfsSample is the smallest sample libblkid probes btrfs in: the btrfs
// prober sets a minimum device size of 1 MiB, and probing functions whose
// minimum is larger than the sample are skipped entirely.
const minBtrfsSample = 1024 * 1024

// How small a sample may be is decided by libblkid, which ignores anything of
// 1 KiB or less and skips probers that declare a larger minimum. The package
// documentation promises exactly this behaviour.
func TestGetFilesystemTypeFromBuffer_MinimumSampleSize(t *testing.T) {
	ext2 := extBuffer(8*1024, 2, 0, 0)

	tests := []struct {
		name   string
		buffer []byte
		want   string
	}{
		{"ext2 in 1024 bytes is ignored", ext2[:1024], ""},
		{"ext2 needs its whole superblock", ext2[:2048], "ext2"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := GetFilesystemTypeFromBuffer(test.buffer)
			if test.want == "" {
				if !errors.Is(err, ErrNoFilesystem) {
					t.Fatalf("GetFilesystemTypeFromBuffer() = %q, %v, want %v", got, err, ErrNoFilesystem)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetFilesystemTypeFromBuffer() error = %v, want %q", err, test.want)
			}
			if got != test.want {
				t.Errorf("GetFilesystemTypeFromBuffer() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGetFilesystemTypeFromBuffer_BtrfsMinimumSize(t *testing.T) {
	head := filesystemImageHead(t, "mkfs.btrfs", []string{"-q", "-f"}, 128, 2*minBtrfsSample)
	if len(head) < 2*minBtrfsSample {
		t.Fatalf("image head is %d bytes, want at least %d", len(head), 2*minBtrfsSample)
	}

	// The primary superblock sits at 64 KiB, but libblkid will not probe btrfs
	// at all in a sample below its 1 MiB minimum.
	if _, err := GetFilesystemTypeFromBuffer(head[:headSize]); !errors.Is(err, ErrNoFilesystem) {
		t.Errorf("probing the first %d bytes error = %v, want %v", headSize, err, ErrNoFilesystem)
	}
	if got, err := GetFilesystemTypeFromBuffer(head[:minBtrfsSample]); err != nil || got != "btrfs" {
		t.Errorf("probing the first %d bytes = %q, %v, want btrfs", minBtrfsSample, got, err)
	}
}

// filesystemImageHead creates a filesystem in a sparse temporary file with the
// given mkfs utility and returns the first headBytes of it, skipping the test
// when the utility is not installed.
func filesystemImageHead(t *testing.T, tool string, args []string, sizeMiB, headBytes int) []byte {
	t.Helper()

	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%s is not installed", tool)
	}

	image := filepath.Join(t.TempDir(), "image")
	file, err := os.Create(image)
	if err != nil {
		t.Fatalf("creating image: %v", err)
	}
	if err := file.Truncate(int64(sizeMiB) << 20); err != nil {
		file.Close()
		t.Fatalf("sizing image: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("closing image: %v", err)
	}

	if out, err := exec.Command(tool, append(args, image)...).CombinedOutput(); err != nil {
		t.Skipf("%s %v failed: %v\n%s", tool, args, err, out)
	}

	file, err = os.Open(image)
	if err != nil {
		t.Fatalf("opening image: %v", err)
	}
	defer file.Close()

	head := make([]byte, headBytes)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("reading image head: %v", err)
	}
	if n < headBytes {
		t.Fatalf("image head is %d bytes, want %d", n, headBytes)
	}
	return head[:n]
}

func randomBytes(size int) []byte {
	buffer := make([]byte, size)
	// A fixed seed keeps this test deterministic: the same bytes are probed on
	// every run.
	source := rand.New(rand.NewSource(1))
	source.Read(buffer)
	return buffer
}

// openFileDescriptors counts the descriptors this process has open.
func openFileDescriptors(t *testing.T) int {
	t.Helper()

	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot count open file descriptors: %v", err)
	}
	return len(entries)
}
