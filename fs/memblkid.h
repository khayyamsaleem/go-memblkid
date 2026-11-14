/*
 * memblkid - probe filesystem superblocks held in memory with libblkid.
 *
 * Every symbol in this library is prefixed with memblkid_ so it cannot collide
 * with the C code of a program that links it.
 */
#ifndef MEMBLKID_H
#define MEMBLKID_H

#include <stddef.h>

#include <blkid/blkid.h>

/*
 * Longest filesystem name reported by libblkid ("promise_fasttrack_raid_member"
 * is the current record at 30 bytes) plus room for the NUL terminator.
 */
#define MEMBLKID_FSTYPE_MAX 64

/*
 * A probe over an in-memory buffer. The file descriptor backing the probe is
 * owned by this struct: it must stay open while the probe is used and is closed
 * by memblkid_free_probe().
 */
struct memblkid_probe {
	blkid_probe pr;
	int fd;
};

/* What a recognized superblock reported. */
struct memblkid_info {
	char fstype[MEMBLKID_FSTYPE_MAX]; /* NUL-terminated, "" if not reported */
	unsigned long long block_size;    /* filesystem block size in bytes, 0 if not reported */
};

/*
 * Creates a probe over a private copy of @size bytes at @buf. The buffer is
 * copied into an anonymous in-memory file (memfd) because libblkid only probes
 * file descriptors; @buf is never modified and is not referenced after this
 * call returns.
 *
 * On failure the returned probe has a NULL pr and, if @err is not NULL, *err is
 * set to a positive errno value.
 */
struct memblkid_probe memblkid_probe_from_buffer(const unsigned char *buf,
						 size_t size, int *err);

/*
 * Releases a probe created by memblkid_probe_from_buffer() and closes the file
 * descriptor behind it. Safe to call on a zeroed or already-freed probe.
 */
void memblkid_free_probe(struct memblkid_probe *mp);

/*
 * Runs the superblock probe chain over @pr and fills @info, which is never left
 * partially written.
 *
 * Returns 0 when a filesystem superblock was recognized, 1 when the buffer holds
 * no filesystem that libblkid knows, and -1 on error. Call it once per probe.
 */
int memblkid_probe_info(blkid_probe pr, struct memblkid_info *info);

/*
 * Returns a newly allocated NULL-terminated array of the names of every
 * filesystem libblkid can recognize, or NULL on allocation failure. @count, if
 * not NULL, receives the number of names. Release it with
 * memblkid_free_supported_filesystems().
 */
char **memblkid_list_supported_filesystems(size_t *count);

/* Releases a list returned by memblkid_list_supported_filesystems(). */
void memblkid_free_supported_filesystems(char **list);

#endif /* MEMBLKID_H */
