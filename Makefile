# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get

# Name of the binary to be built
BINARY_NAME=myproject

# Directories
CMD_DIR=./cmd
BIN_DIR=./bin
SRC_DIRS := $(shell find . -name '*.go' -not -path "./vendor/*")

# Build flags
BUILD_TAGS=

# Build the project
build:
	$(GOBUILD) -tags "$(BUILD_TAGS)" -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_DIR)/main.go

# Clean the project
clean:
	$(GOCLEAN)
	rm -rf $(BIN_DIR)

# Test the project
test:
	$(GOTEST) -v ./...


# Test the project
docker-run:
	docker rm -f nimiq-test || true && docker run -d --user root --name nimiq-test -v /mnt/c/Users/super/OneDrive/Bureaublad/projects/snapshot-service/test/:/root/.nimiq/ maestroi/nimiq-albatross:stable

# Install project dependencies
deps:
	$(GOGET) -u

.PHONY: build clean test deps
