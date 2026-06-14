VERSION ?= dev
BINARY  := agenthubctl
BUILD_FLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build install clean

build:
	go build $(BUILD_FLAGS) -o $(BINARY) ./cmd/agenthubctl

# install は go install で GOBIN (なければ GOPATH/bin) に配置し、
# その場所が PATH 上にあるかを検査して導線を案内する (issue #38)。
install:
	go install $(BUILD_FLAGS) ./cmd/agenthubctl
	@GOBIN_DIR="$$(go env GOBIN)"; \
	if [ -z "$$GOBIN_DIR" ]; then GOBIN_DIR="$$(go env GOPATH)/bin"; fi; \
	echo "installed $(BINARY) -> $$GOBIN_DIR/$(BINARY)"; \
	case ":$$PATH:" in \
	  *":$$GOBIN_DIR:"*) echo "PATH OK: $$GOBIN_DIR is on PATH";; \
	  *) echo "WARNING: $$GOBIN_DIR is NOT on PATH."; \
	     echo "         Add it so 'agenthubctl' resolves uniquely:"; \
	     echo "         export PATH=\"$$GOBIN_DIR:\$$PATH\"";; \
	esac
	@echo "verify with: agenthubctl version"

clean:
	rm -f $(BINARY)
