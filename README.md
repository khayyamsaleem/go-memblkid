# go-memblkid

[![CI](https://github.com/khayyamsaleem/go-memblkid/actions/workflows/ci.yml/badge.svg)](https://github.com/khayyamsaleem/go-memblkid/actions/workflows/ci.yml)

Identify the filesystem held in a block of memory, using
[libblkid](https://github.com/util-linux/util-linux) — the library behind
`blkid(8)` and `lsblk(8)` — without opening the device it came from and without
root.

`go-memblkid` takes a byte slice, usually the head of a device, image or archive
member, and reports what it is: `ext4`, `xfs`, `vfat`, `btrfs`, `LVM2_member`,
and every other type libblkid knows.

## Requirements

- Linux, built with `CGO_ENABLED=1` — see [Portability](#portability)
- Go 1.22 or newer
- libblkid development files and pkg-config

| Distribution | Install |
| --- | --- |
| Debian / Ubuntu | `apt-get install libblkid-dev` |
| Fedora / RHEL | `dnf install libblkid-devel` |
| openSUSE | `zypper install libblkid-devel` |
| Arch / Manjaro | `pacman -S util-linux` |

## Installation

```
go get github.com/khayyamsaleem/go-memblkid
```

## Usage

```go
import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/khayyamsaleem/go-memblkid/fs"
)

// sample is a read-only copy of the start of whatever holds the filesystem.
sample := make([]byte, 64*1024)

fsType, err := fs.GetFilesystemTypeFromBuffer(sample)
switch {
case errors.Is(err, fs.ErrNoFilesystem):
	log.Print("nothing recognizable in these bytes")
case err != nil:
	log.Fatal(err)
default:
	fmt.Println(fsType) // ext4
}

blockSize, err := fs.GetBlockSizeFromBuffer(sample)
if err == nil {
	fmt.Println(blockSize) // 4096
}
```

Reading a device first, with a sample big enough for every filesystem libblkid
can recognize:

```go
file, err := os.Open("/dev/nvme0n1")
if err != nil {
	log.Fatal(err)
}
defer file.Close()

sample := make([]byte, 1024*1024)
n, err := io.ReadFull(file, sample)
if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
	log.Fatal(err)
}
sample = sample[:n]

fsType, err := fs.GetFilesystemTypeFromBuffer(sample)
```

### How big should the sample be?

Size decides what can be found. The rules below come from libblkid itself; the
first two are covered by the tests, the rest were measured against it.

| Sample | What it can recognize |
| --- | --- |
| 1 KiB or less | nothing: libblkid ignores areas that small |
| ~2 KiB | ext2, ext3, ext4, XFS and vfat, whose superblocks are near the start |
| 64 KiB | everything else with a superblock in the first 64 KiB |
| 1 MiB | adds btrfs, which libblkid will not probe below that size |
| > 1440 KiB | adds RAID-usage types such as `LVM2_member`, which libblkid skips in "tiny" areas of 1440 KiB or less |
| more | some filesystems are validated with structures deeper in the volume — libblkid reads an HFS+ catalog B-tree, for instance — so a head sample may not identify them at all |

Too small is not an error: the probe reports `fs.ErrNoFilesystem`, and
`cmd/blkidscan` reads 2 MiB to stay above the thresholds that matter.

### Restricting the probe

When you already expect one filesystem, filtering is faster and cannot
mis-detect:

```go
fsType, err := fs.GetFilesystemTypeFromBuffer(sample, fs.WithFilesystemFilter("ext4", "xfs"))
```

The names are the ones `fs.SupportedFilesystems()` returns; a name libblkid does
not know simply never matches.

## API

| Function | Purpose |
| --- | --- |
| `GetFilesystemTypeFromBuffer(data []byte, options ...ProbeOption) (string, error)` | the filesystem type, or an error |
| `GetBlockSizeFromBuffer(data []byte, options ...ProbeOption) (uint64, error)` | its allocation unit in bytes |
| `SupportedFilesystems() []string` | every name libblkid recognizes, sorted |
| `WithFilesystemFilter(filesystems ...string) ProbeOption` | limit a single probe to some types |
| `ErrNoFilesystem` | the probe ran and recognized nothing |
| `ErrNoBlockSize` | a filesystem was recognized but reports no block size |

Guarantees:

- **Safe for concurrent use.** No global or cached state: each call probes its
  own copy with its own libblkid state, so goroutines cannot interfere.
- **The caller keeps its buffer.** Probing copies the sample into a private
  anonymous in-memory file (`memfd`) and never writes to or retains the slice.
- **No leaked resources.** Every probe, buffer and file descriptor is released
  before the call returns, including on error paths.

### Block size, precisely

`GetBlockSizeFromBuffer` reports libblkid's `FSBLOCKSIZE` when a filesystem
provides one, and falls back to `BLOCK_SIZE` otherwise. The difference matters:
libblkid's `BLOCK_SIZE` is the *sector* size for some filesystems, so `blkid(8)`
reports 512 for an XFS filesystem with 4096-byte blocks, while this package
reports 4096. For vfat the value is the cluster size.

## Command line

`cmd/blkidscan` prints the type and block size of block devices or image files,
scanning the block devices directly under `/dev` when given no arguments:

```
$ sudo go run ./cmd/blkidscan
/dev/nvme0n1: type=ext4 block_size=4096
/dev/nvme0n1p1: type=vfat block_size=512

$ go run ./cmd/blkidscan disk.img
disk.img: type=xfs block_size=4096
```

## How it works

libblkid only probes file descriptors, so `memblkid` (the small C library in
[`fs/memblkid.c`](fs/memblkid.c)) copies the sample into an anonymous in-memory
file, points a `blkid_probe` at that descriptor, asks every superblock prober in
turn for its verdict, and copies the answer into a fixed-size struct. The Go
layer above it owns the memory, maps libblkid's return codes onto Go errors and
turns the C strings into Go strings.

Two details worth knowing:

- libblkid reports the result of **one** prober per call, so the C code loops
  until a prober recognizes a superblock or the list runs out.
- libblkid does not close a descriptor it did not open, so the file descriptor
  behind each probe is owned by the probe and closed with it.

Every C symbol is prefixed with `memblkid_` so the library cannot collide with
other C code in a program that links it.

## Portability

Probing needs libblkid and cgo, so the `fs` package is built for Linux with
`CGO_ENABLED=1` only and carries a `//go:build linux && cgo` constraint. On other
platforms importing it fails with "build constraints exclude all Go files",
which is deliberate: there is no useful fallback, and a package directory that
holds a `.c` file cannot be built without cgo. Guard the files that use it:

```go
//go:build linux

package yourapp
```

## Testing

The Go tests build real ext4, XFS, vfat and btrfs images with `mkfs` tools
(skipping the cases whose tool is missing), probe hand-built superblocks,
exercise the filter and error paths, check that probing never modifies the
caller's buffer, run under the race detector, count open file descriptors before
and after 600 probes to catch leaks, and fuzz the entry point. The C library has
its own tests that need neither Go nor root.

```
make test                 # go test -race ./... and the C tests
make test-go
make test-c
make test-c-sanitize      # the C tests with ASan and UBSan
go test -asan ./...       # the Go tests with ASan
go test -run '^$' -fuzz FuzzGetFilesystemTypeFromBuffer -fuzztime 30s ./fs
```

No Linux development machine? The same suite runs in a container:

```
docker run --rm -it -v "$PWD":/src -w /src ubuntu:24.04 bash -c '
  apt-get update &&
  apt-get install -y golang-go libblkid-dev pkg-config make \
    e2fsprogs xfsprogs dosfstools btrfs-progs &&
  make test'
```

Everything above is what the CI workflow runs, on the oldest supported Go and on
the current one.

## Contributing

Go nuts. `make test` before a pull request.

## License

MIT — see [LICENSE](LICENSE).

## Acknowledgements

Thank you to the libblkid / [util-linux](https://github.com/util-linux/util-linux)
contributors. I stand on the shoulders of giants.
