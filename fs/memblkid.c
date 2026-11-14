/*
 * memblkid - probe filesystem superblocks held in memory with libblkid.
 */
#define _GNU_SOURCE

#include <errno.h>
#include <fcntl.h>
#include <linux/memfd.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

#include "memblkid.h"

#define MEMBLKID_MEMFD_NAME "memblkid-probe"

/*
 * write(2) may write fewer bytes than requested and may be interrupted by a
 * signal, so loop until the whole buffer is written: a short write must never
 * reach libblkid, which decides what the buffer holds from its size.
 */
static int write_all(int fd, const unsigned char *buf, size_t size)
{
	while (size > 0) {
		ssize_t written = write(fd, buf, size);

		if (written < 0) {
			if (errno == EINTR)
				continue;
			return -1;
		}
		if (written == 0) {
			errno = EIO;
			return -1;
		}
		buf += written;
		size -= (size_t) written;
	}
	return 0;
}

struct memblkid_probe memblkid_probe_from_buffer(const unsigned char *buf,
						 size_t size, int *err)
{
	struct memblkid_probe mp = { .pr = NULL, .fd = -1 };

	if (err)
		*err = 0;

	if (!buf || size == 0) {
		if (err)
			*err = EINVAL;
		return mp;
	}

	/* memfd_create(2) through syscall() rather than the glibc wrapper, so
	 * the library also builds against C libraries that predate it. */
	mp.fd = (int) syscall(SYS_memfd_create, MEMBLKID_MEMFD_NAME, MFD_CLOEXEC);
	if (mp.fd < 0)
		goto fail;

	if (write_all(mp.fd, buf, size) != 0)
		goto fail;

	mp.pr = blkid_new_probe();
	if (!mp.pr) {
		errno = ENOMEM;
		goto fail;
	}

	/*
	 * blkid_probe_set_device() reads through the descriptor lazily while
	 * probing, and only closes descriptors it opened itself, so it does not
	 * take ownership of mp.fd. The descriptor stays open until
	 * memblkid_free_probe() runs.
	 */
	if (blkid_probe_set_device(mp.pr, mp.fd, 0, 0) != 0)
		goto fail;

#ifdef BLKID_SUBLKS_FSINFO
	/*
	 * Report the filesystem's own sizes as well as the superblock type. This
	 * keeps libblkid's default set of values and adds FSBLOCKSIZE and
	 * friends; see memblkid_probe_info(). Guarded so the library still
	 * builds against libblkid releases that predate the flag.
	 */
	blkid_probe_set_superblocks_flags(mp.pr,
					  BLKID_SUBLKS_DEFAULT | BLKID_SUBLKS_FSINFO);
#endif

	if (err)
		*err = 0;
	return mp;

fail:
	if (err)
		*err = errno > 0 ? errno : EINVAL;
	blkid_free_probe(mp.pr);
	if (mp.fd >= 0)
		close(mp.fd);
	mp.pr = NULL;
	mp.fd = -1;
	return mp;
}

void memblkid_free_probe(struct memblkid_probe *mp)
{
	if (!mp)
		return;
	blkid_free_probe(mp->pr);
	if (mp->fd >= 0)
		close(mp->fd);
	mp->pr = NULL;
	mp->fd = -1;
}

/*
 * Looks up a numeric NAME=value pair reported by the current probing result,
 * returning 0 when it is not there. libblkid stores these as decimal strings.
 */
static unsigned long long lookup_u64(blkid_probe pr, const char *name)
{
	const char *value = NULL;

	if (!blkid_probe_has_value(pr, name))
		return 0;
	if (blkid_probe_lookup_value(pr, name, &value, NULL) != 0 || !value)
		return 0;
	return strtoull(value, NULL, 10);
}

int memblkid_probe_info(blkid_probe pr, struct memblkid_info *info)
{
	int rc;

	if (!pr || !info) {
		errno = EINVAL;
		return -1;
	}

	memset(info, 0, sizeof(*info));

	/*
	 * blkid_do_probe() reports the result of a single probing routine per
	 * call and BLKID_PROBE_NONE once every prober has been tried, so it has
	 * to be called in a loop. The first prober that recognizes a superblock
	 * and reports a type wins; its sizes come from that same probing result.
	 */
	while ((rc = blkid_do_probe(pr)) == BLKID_PROBE_OK) {
		const char *type = NULL;
		size_t len = 0;

		if (!blkid_probe_has_value(pr, "TYPE"))
			continue;

		if (blkid_probe_lookup_value(pr, "TYPE", &type, &len) != 0 || !type)
			continue;
		if (len == 0 || len > sizeof(info->fstype))
			continue; /* unusable name: never report a truncated type */

		memcpy(info->fstype, type, len);

		/*
		 * FSBLOCKSIZE is the allocation unit of the filesystem itself.
		 * BLOCK_SIZE, which libblkid reports for every filesystem it
		 * recognizes, is the sector size for some of them (XFS and vfat
		 * report 512 there for a filesystem with 4096-byte blocks), so it
		 * is only a fallback.
		 */
		info->block_size = lookup_u64(pr, "FSBLOCKSIZE");
		if (info->block_size == 0)
			info->block_size = lookup_u64(pr, "BLOCK_SIZE");

		return 0;
	}

	return rc == BLKID_PROBE_NONE ? 1 : -1;
}

char **memblkid_list_supported_filesystems(size_t *count)
{
	const char *name = NULL;
	char **list = NULL;
	size_t total = 0;
	size_t used = 0;
	size_t i;

	if (count)
		*count = 0;

	/* Names live in a static table, so ask how many entries it has and then
	 * copy them out. Nothing is cached between calls: the caller owns the
	 * result and decides when it goes away. */
	while (blkid_superblocks_get_name(total, &name, NULL) == 0)
		total++;

	list = calloc(total + 1, sizeof(*list));
	if (!list)
		return NULL;

	for (i = 0; i < total; i++) {
		if (blkid_superblocks_get_name(i, &name, NULL) != 0 || !name)
			continue;

		list[used] = strdup(name);
		if (!list[used]) {
			memblkid_free_supported_filesystems(list);
			return NULL;
		}
		used++;
	}

	list[used] = NULL;
	if (count)
		*count = used;
	return list;
}

void memblkid_free_supported_filesystems(char **list)
{
	size_t i;

	if (!list)
		return;
	for (i = 0; list[i]; i++)
		free(list[i]);
	free(list);
}
