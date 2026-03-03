BINARY ?= ocr
BUILD_DIR ?= bin
PKG ?= ./cmd/ocr

.PHONY: build install lint test

build:
	mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY) $(PKG)

install:
	go install $(PKG)

lint:
	go vet ./...

test:
	go test ./...
