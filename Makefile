VERSION ?= dev
BINARY  := agenthubctl
BUILD_FLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build install clean

build:
	go build $(BUILD_FLAGS) -o $(BINARY) ./cmd/agenthubctl

install:
	go install $(BUILD_FLAGS) ./cmd/agenthubctl

clean:
	rm -f $(BINARY)
