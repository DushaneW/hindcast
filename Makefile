VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build test lint dist clean

build:
	go build -ldflags "$(LDFLAGS)" -o hindcast ./cmd/hindcast

test:
	go test -race ./...

lint:
	go vet ./...
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

# Release archives and checksums, named the way install.sh and install.ps1
# expect: hindcast_<os>_<arch>.tar.gz (zip on Windows) and checksums.txt.
dist: clean
	mkdir -p dist/stage
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		name=hindcast_$${os}_$${arch}; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/stage/$$name/hindcast$$ext ./cmd/hindcast || exit 1; \
		cp LICENSE README.md dist/stage/$$name/; \
		if [ $$os = windows ]; then (cd dist/stage && zip -qr ../$$name.zip $$name); \
		else tar -C dist/stage -czf dist/$$name.tar.gz $$name; fi; \
	done
	rm -r dist/stage
	cd dist && sha256sum hindcast_* > checksums.txt

clean:
	rm -f hindcast
	rm -fr dist
