VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 linux/arm darwin/arm64 darwin/amd64 windows/amd64 windows/arm64

.PHONY: build run test vet build-all docker clean

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o stats-lite .

run: build
	./stats-lite

test:
	go test ./...

vet:
	go vet ./...

build-all:
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		out=dist/stats-lite-$$os-$$arch$$ext; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" -o $$out . || exit 1; \
	done

docker:
	docker build --build-arg VERSION=$(VERSION) -t stats-lite:$(VERSION) -t stats-lite:latest .

clean:
	rm -rf dist stats-lite stats-lite.exe
