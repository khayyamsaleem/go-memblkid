# Everything the module needs, plus the utilities the tests use to build real
# filesystem images.
GO ?= go
CC ?= cc
PKG_CONFIG ?= pkg-config

BLKID_CFLAGS := $(shell $(PKG_CONFIG) --cflags blkid 2>/dev/null)
BLKID_LIBS := $(shell $(PKG_CONFIG) --libs blkid 2>/dev/null)
ifeq ($(strip $(BLKID_LIBS)),)
BLKID_LIBS := -lblkid
endif

CFLAGS ?= -Wall -Wextra -pedantic -O2
CFLAGS += -I fs $(BLKID_CFLAGS)

BUILD_DIR := build
C_TEST := $(BUILD_DIR)/memblkid_test

.PHONY: all build test test-go test-c test-c-sanitize fmt vet clean

all: build

build:
	$(GO) build ./...

# Go tests need Linux with cgo and libblkid; the C tests need only a C compiler.
test: test-go test-c

test-go:
	$(GO) test -race ./...

test-c: $(C_TEST)
	./$(C_TEST)

$(C_TEST): fs/memblkid.c fs/memblkid.h ctests/memblkid_test.c
	@mkdir -p $(BUILD_DIR)
	$(CC) $(CFLAGS) -o $@ fs/memblkid.c ctests/memblkid_test.c $(BLKID_LIBS)

# Same checks with the memory and undefined behaviour sanitizers turned on.
test-c-sanitize:
	@mkdir -p $(BUILD_DIR)
	$(CC) $(CFLAGS) -g -O1 -fsanitize=address,undefined -fno-omit-frame-pointer \
		-o $(BUILD_DIR)/memblkid_test_sanitize fs/memblkid.c ctests/memblkid_test.c $(BLKID_LIBS)
	./$(BUILD_DIR)/memblkid_test_sanitize

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf $(BUILD_DIR)
