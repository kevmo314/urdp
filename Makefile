WIN_ENV = CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
	PKG_CONFIG_LIBDIR=$(CURDIR)/third_party/windows/lib/pkgconfig

.PHONY: all linux windows windows-deps test vet vet-windows clean

all: linux

linux:
	go build -o bin/urdp .

windows-deps:
	scripts/build-windows-deps.sh

windows: windows-deps
	$(WIN_ENV) go build -ldflags "-s -w" -o bin/urdp.exe .

test:
	go test -race ./...

vet:
	go vet ./...

vet-windows: windows-deps
	$(WIN_ENV) go vet ./...

clean:
	rm -rf bin

# Build the codec archives for macOS 13: current Homebrew bottles can have a
# newer deployment target, even when the final Go link requests macOS 13.
MAC_DEPS = $(CURDIR)/third_party/darwin
MAC_X264_REV = b35605ace3ddf7c1a5d67a2eb553f034aef41d55
MAC_OPUS_VERSION = 1.6.1
MAC_ENV = MACOSX_DEPLOYMENT_TARGET=13.0 CFLAGS="-O3 -mmacosx-version-min=13.0"

.PHONY: mac mac-deps
mac-deps: $(MAC_DEPS)/lib/libx264.a $(MAC_DEPS)/lib/libopus.a

$(MAC_DEPS)/lib/libx264.a:
	@mkdir -p $(MAC_DEPS)/src
	curl -fL https://code.videolan.org/videolan/x264/-/archive/$(MAC_X264_REV)/x264-$(MAC_X264_REV).tar.gz -o $(MAC_DEPS)/src/x264.tar.gz
	rm -rf $(MAC_DEPS)/src/x264-$(MAC_X264_REV)
	tar -xzf $(MAC_DEPS)/src/x264.tar.gz -C $(MAC_DEPS)/src
	cd $(MAC_DEPS)/src/x264-$(MAC_X264_REV) && $(MAC_ENV) ./configure --prefix=$(MAC_DEPS) --enable-static --disable-shared --disable-cli --disable-opencl
	MACOSX_DEPLOYMENT_TARGET=13.0 $(MAKE) -C $(MAC_DEPS)/src/x264-$(MAC_X264_REV) -j$$(sysctl -n hw.logicalcpu)
	MACOSX_DEPLOYMENT_TARGET=13.0 $(MAKE) -C $(MAC_DEPS)/src/x264-$(MAC_X264_REV) install

$(MAC_DEPS)/lib/libopus.a:
	@mkdir -p $(MAC_DEPS)/src
	curl -fL https://ftp.osuosl.org/pub/xiph/releases/opus/opus-$(MAC_OPUS_VERSION).tar.gz -o $(MAC_DEPS)/src/opus.tar.gz
	@echo "6ffcb593207be92584df15b32466ed64bbec99109f007c82205f0194572411a1  $(MAC_DEPS)/src/opus.tar.gz" | shasum -a 256 -c -
	rm -rf $(MAC_DEPS)/src/opus-$(MAC_OPUS_VERSION)
	tar -xzf $(MAC_DEPS)/src/opus.tar.gz -C $(MAC_DEPS)/src
	cd $(MAC_DEPS)/src/opus-$(MAC_OPUS_VERSION) && $(MAC_ENV) ./configure --prefix=$(MAC_DEPS) --enable-static --disable-shared --disable-extra-programs --disable-doc
	MACOSX_DEPLOYMENT_TARGET=13.0 $(MAKE) -C $(MAC_DEPS)/src/opus-$(MAC_OPUS_VERSION) -j$$(sysctl -n hw.logicalcpu)
	MACOSX_DEPLOYMENT_TARGET=13.0 $(MAKE) -C $(MAC_DEPS)/src/opus-$(MAC_OPUS_VERSION) install

# --static alone emits -lfoo, which prefers a dylib if one exists. Replace
# those flags with archive paths, and rebuild cgo to avoid cached dynamic flags.
mac: mac-deps
	@mkdir -p bin
	@printf '%s\n' '#!/bin/sh' 'set -e' 'flags=$$(pkg-config --static "$$@")' 'printf "%s\n" "$$flags" | sed "s|-lx264|$(MAC_DEPS)/lib/libx264.a|g; s|-lopus|$(MAC_DEPS)/lib/libopus.a|g"' > bin/pkg-config-darwin
	@chmod +x bin/pkg-config-darwin
	MACOSX_DEPLOYMENT_TARGET=13.0 CGO_ENABLED=1 CGO_CFLAGS="-O2 -g -mmacosx-version-min=13.0" PKG_CONFIG_LIBDIR="$(MAC_DEPS)/lib/pkgconfig" PKG_CONFIG_PATH="$(MAC_DEPS)/lib/pkgconfig" PKG_CONFIG="$(CURDIR)/bin/pkg-config-darwin" go build -a -ldflags "-s -w" -o bin/urdp .
