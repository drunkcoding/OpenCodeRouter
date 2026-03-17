BINARY ?= opencoderouter
BUILD_DIR ?= bin
PKG ?= .

.PHONY: build install lint test run

build:
	mkdir -p $(BUILD_DIR)
	GOFLAGS="-buildvcs=false" go build -o $(BUILD_DIR)/$(BINARY) $(PKG)

install:
	GOFLAGS="-buildvcs=false" go install $(PKG)

lint:
	go vet ./...

test:
	go test ./...

run:
	go run . $(ARGS)
