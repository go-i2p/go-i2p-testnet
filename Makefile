# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get

# Binary names
BINARY_NAME1=go-i2p-testnet
BINARY_DIR=bin
# Build directory
BUILD_DIR=bin

# Main packages
MAIN1=main.go
# Targets
.PHONY: all build clean test run dashboard install uninstall

all: test build

build:
	mkdir -p $(BUILD_DIR)
	$(GOBUILD) -o $(BUILD_DIR)/$(BINARY_NAME1) -v $(MAIN1)

clean:
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)

test:
	$(GOTEST) -v ./...

run: build
	cd $(BUILD_DIR) && ./$(BINARY_NAME1)
# Run only the status dashboard against an already-running testnet
# (routers are discovered from the live Docker topology).
dashboard: build
	cd $(BUILD_DIR) && ./$(BINARY_NAME1) -dashboard
