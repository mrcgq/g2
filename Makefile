BINARY := phantom-server
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y-%m-%d %H:%M:%S')

LDFLAGS := -s -w \
	-X 'main.Version=$(VERSION)' \
	-X 'main.BuildTime=$(BUILD_TIME)' \
	-X 'main.GitCommit=$(COMMIT)'

.PHONY: all build clean test lint run release gen-psk

all: build

build:
	@echo "🔨 构建 $(BINARY)..."
	@go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/phantom-server
	@echo "✅ 完成: $(BINARY)"

release:
	@echo "🚀 构建多平台版本..."
	@mkdir -p dist
	@for platform in "linux/amd64" "linux/arm64" "darwin/amd64" "darwin/arm64"; do \
		GOOS=$${platform%/*} GOARCH=$${platform#*/} CGO_ENABLED=0 \
		go build -trimpath -ldflags "$(LDFLAGS)" \
		-o dist/$(BINARY)-$${platform%/*}-$${platform#*/} ./cmd/phantom-server; \
	done
	@echo "✅ 完成"
	@ls -lh dist/

clean:
	@rm -f $(BINARY)
	@rm -rf dist/
	@rm -f coverage.out

test:
	@go test -v -race -coverprofile=coverage.out ./...

lint:
	@go vet ./...
	@golangci-lint run

run: build
	@./$(BINARY) -c configs/config.example.yaml

gen-psk:
	@./$(BINARY) -gen-psk 2>/dev/null || openssl rand -base64 32

install: build
	@sudo cp $(BINARY) /usr/local/bin/
	@echo "✅ 安装完成"


