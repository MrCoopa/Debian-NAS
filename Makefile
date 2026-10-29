.PHONY: all build build-linux run-mock clean

BINARY_NAME=nasd
BACKEND_DIR=backend

all: build

build:
	@echo "Building local binary..."
	cd $(BACKEND_DIR) && go build -o bin/$(BINARY_NAME) cmd/nasd/main.go

build-linux:
	@echo "Building static Linux amd64 binary for Debian 13..."
	cd $(BACKEND_DIR) && env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$(BINARY_NAME)-linux-amd64 cmd/nasd/main.go

build-linux-arm64:
	@echo "Building static Linux arm64 binary (e.g. Raspberry Pi 5 / RK3588)..."
	cd $(BACKEND_DIR) && env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$(BINARY_NAME)-linux-arm64 cmd/nasd/main.go

run-mock:
	@echo "Starting daemon in simulated mock mode (Port 8080)..."
	cd $(BACKEND_DIR) && go run cmd/nasd/main.go -mock -port 8080

clean:
	rm -rf $(BACKEND_DIR)/bin
