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
