.PHONY: all build build-linux deploy run-mock clean css watch-css

BINARY_NAME=nasd
BACKEND_DIR=backend
INSTALL_DIR=/opt/debian-nas

all: build

css:
	@chmod +x scripts/build-css.sh 2>/dev/null || true
	@./scripts/build-css.sh

watch-css:
	@chmod +x scripts/build-css.sh 2>/dev/null || true
	@./scripts/build-css.sh --watch

build:
	@echo "Building Debian Linux binary into bin/$(BINARY_NAME)..."
	mkdir -p bin
	cd $(BACKEND_DIR) && CGO_ENABLED=0 go build -ldflags="-s -w" -o ../bin/$(BINARY_NAME) ./cmd/nasd

build-linux:
	@echo "Building static Linux amd64 binary for Debian 13..."
	mkdir -p bin
	cd $(BACKEND_DIR) && env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o ../bin/$(BINARY_NAME)-linux-amd64 ./cmd/nasd

deploy: build
	@echo "Restarting systemd service nasd.service..."
	systemctl restart nasd.service

run-mock:
	@echo "Starting daemon in simulated mock mode (Port 8080)..."
	cd $(BACKEND_DIR) && go run ./cmd/nasd -mock -port 8080

clean:
	rm -rf bin $(BACKEND_DIR)/bin
