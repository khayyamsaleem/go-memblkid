/*
 * Tests for the memblkid C library, run without Go and without root.
 *
 * Build and run them with "make test-c" from the repository root; the same file
 * is compiled with the address and undefined behaviour sanitizers by
 * "make test-c-sanitize".
 */
#define _GNU_SOURCE

#include <dirent.h>
#include <errno.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "memblkid.h"

#define SAMPLE_SIZE (64 * 1024)

/* Fields of the ext-series superblock, which starts 1024 bytes in. */
#define EXT_LOG_BLOCK_SIZE_OFF (1024 + 0x18)
#define EXT_MAGIC_OFF          (1024 + 0x38)

#define EXT_MAGIC     0xEF53
#define EXT_BLOCK_LOG 2 /* block size = 1024 << 2 = 4096 */

static int failures;

static void check(int condition, const char *what)
{
	if (condition)
		return;
	fprintf(stderr, "FAIL: %s\n", what);
	failures++;
}

/* Little-endian writers, so the tests do not depend on host byte order. */
static void put_le16(unsigned char *buf, size_t off, uint16_t value)
{
	buf[off] = (unsigned char) (value & 0xff);
	buf[off + 1] = (unsigned char) ((value >> 8) & 0xff);
}

static void put_le32(unsigned char *buf, size_t off, uint32_t value)
{
	buf[off] = (unsigned char) (value & 0xff);
	buf[off + 1] = (unsigned char) ((value >> 8) & 0xff);
	buf[off + 2] = (unsigned char) ((value >> 16) & 0xff);
	buf[off + 3] = (unsigned char) ((value >> 24) & 0xff);
}

/* Returns the number of open file descriptors, or -1 without /proc. */
static int open_fds(void)
{
	DIR *dir;
	struct dirent *entry;
	int count = 0;

	dir = opendir("/proc/self/fd");
	if (!dir)
		return -1;
	while ((entry = readdir(dir)) != NULL)
		count++;
	closedir(dir);
	return count;
}

/* Returns a 64 KiB buffer holding an ext2 filesystem with 4 KiB blocks. */
static unsigned char *ext_sample(void)
{
	unsigned char *buf = calloc(1, SAMPLE_SIZE);

	if (!buf) {
		perror("calloc");
		exit(EXIT_FAILURE);
	}
	put_le16(buf, EXT_MAGIC_OFF, EXT_MAGIC);
	put_le32(buf, EXT_LOG_BLOCK_SIZE_OFF, EXT_BLOCK_LOG);
	return buf;
}

static void test_probe_from_buffer(void)
{
	struct memblkid_probe mp;
	struct memblkid_info info;
	unsigned char *buf = ext_sample();
	int err = -1;

	mp = memblkid_probe_from_buffer(buf, SAMPLE_SIZE, &err);
	check(err == 0, "a valid buffer is accepted without error");
	check(mp.pr != NULL, "a probe is created for a valid buffer");
	check(mp.fd >= 0, "the probe owns the descriptor behind it");

	check(memblkid_probe_info(mp.pr, &info) == 0, "the ext sample is recognized");
	check(strcmp(info.fstype, "ext2") == 0, "the recognized type is ext2");
	check(info.block_size == 4096, "the reported block size is 4096");

	memblkid_free_probe(&mp);
	check(mp.pr == NULL && mp.fd == -1, "freeing a probe clears it");
	memblkid_free_probe(&mp); /* must be harmless the second time */

	free(buf);
}

static void test_no_filesystem(void)
{
	struct memblkid_probe mp;
	struct memblkid_info info;
	unsigned char *buf = calloc(1, SAMPLE_SIZE);

	if (!buf) {
		perror("calloc");
		exit(EXIT_FAILURE);
	}

	mp = memblkid_probe_from_buffer(buf, SAMPLE_SIZE, NULL);
	check(mp.pr != NULL, "a zeroed buffer can still be probed");
	check(memblkid_probe_info(mp.pr, &info) == 1, "a zeroed buffer holds no filesystem");
	check(info.fstype[0] == '\0', "no type is reported for a zeroed buffer");
	check(info.block_size == 0, "no block size is reported for a zeroed buffer");
	memblkid_free_probe(&mp);

	free(buf);
}

static void test_bad_arguments(void)
{
	struct memblkid_probe mp;
	struct memblkid_info info;
	int err = 0;

	mp = memblkid_probe_from_buffer(NULL, SAMPLE_SIZE, &err);
	check(mp.pr == NULL && mp.fd == -1, "a NULL buffer is refused");
	check(err == EINVAL, "a NULL buffer reports EINVAL");

	mp = memblkid_probe_from_buffer((const unsigned char *) "x", 0, &err);
	check(mp.pr == NULL, "an empty buffer is refused");
	check(err == EINVAL, "an empty buffer reports EINVAL");

	check(memblkid_probe_info(NULL, &info) == -1, "probing a NULL probe fails");
	check(memblkid_probe_info(NULL, NULL) == -1, "probing into NULL fails");
}

static void test_filesystem_filter(void)
{
	struct memblkid_probe mp;
	struct memblkid_info info;
	unsigned char *buf = ext_sample();
	char *only_xfs[] = { "xfs", NULL };
	char *with_ext2[] = { "ext2", "xfs", NULL };

	mp = memblkid_probe_from_buffer(buf, SAMPLE_SIZE, NULL);
	check(mp.pr != NULL, "a probe is created for the filter test");

	check(blkid_probe_filter_superblocks_type(mp.pr, BLKID_FLTR_ONLYIN, only_xfs) == 0,
	      "the filter is accepted");
	check(memblkid_probe_info(mp.pr, &info) == 1, "an ext2 sample filtered to xfs has no filesystem");

	check(blkid_probe_filter_superblocks_type(mp.pr, BLKID_FLTR_ONLYIN, with_ext2) == 0,
	      "a second filter is accepted");
	check(memblkid_probe_info(mp.pr, &info) == 0, "an ext2 sample filtered to ext2 is recognized");
	check(strcmp(info.fstype, "ext2") == 0, "the filtered probe reports ext2");

	memblkid_free_probe(&mp);
	free(buf);
}

static void test_supported_filesystems(void)
{
	size_t count = 0;
	int saw_ext4 = 0;
	int i;

	for (i = 0; i < 2; i++) { /* twice: nothing may be cached between calls */
		char **list = memblkid_list_supported_filesystems(&count);
		size_t n;

		check(list != NULL, "the supported filesystems can be listed");
		check(count > 0, "at least one filesystem is supported");
		if (!list)
			continue;

		for (n = 0; n < count; n++) {
			check(list[n] != NULL, "no entry in the list is NULL");
			if (list[n] && strcmp(list[n], "ext4") == 0)
				saw_ext4 = 1;
		}
		check(list[count] == NULL, "the list is NULL-terminated");
		memblkid_free_supported_filesystems(list);
	}

	check(count > 0 && saw_ext4, "ext4 is among the supported filesystems");
	memblkid_free_supported_filesystems(NULL); /* must be harmless */
}

static void test_probes_leak_no_descriptors(void)
{
	unsigned char *buf = ext_sample();
	int before, after;
	int i;

	for (i = 0; i < 10; i++) { /* warm up anything opened lazily */
		struct memblkid_probe mp = memblkid_probe_from_buffer(buf, SAMPLE_SIZE, NULL);

		memblkid_free_probe(&mp);
	}

	before = open_fds();
	for (i = 0; i < 250; i++) {
		struct memblkid_probe mp = memblkid_probe_from_buffer(buf, SAMPLE_SIZE, NULL);

		check(mp.pr != NULL, "every probe is created");
		memblkid_free_probe(&mp);
	}
	after = open_fds();

	if (before < 0 || after < 0) {
		printf("skipping the descriptor leak check: no /proc/self/fd\n");
	} else {
		if (after != before)
			fprintf(stderr, "open descriptors: %d before, %d after 250 probes\n", before, after);
		check(after == before, "probing does not leak file descriptors");
	}

	free(buf);
}

int main(void)
{
	test_probe_from_buffer();
	test_no_filesystem();
	test_bad_arguments();
	test_filesystem_filter();
	test_supported_filesystems();
	test_probes_leak_no_descriptors();

	if (failures) {
		fprintf(stderr, "%d check(s) failed\n", failures);
		return EXIT_FAILURE;
	}
	printf("all memblkid C checks passed\n");
	return EXIT_SUCCESS;
}
