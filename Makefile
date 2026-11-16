VERSION ?= dev
BINARY  := agenthubctl
BUILD_FLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build install clean

build:
	go build $(BUILD_FLAGS) -o $(BINARY) ./cmd/agenthubctl

# install は ~/.local/bin (INSTALL_DIR) に配置する。GOBIN/GOPATH や mise の Go install dir は
# Go の設定・版で場所が変わるため入れ先にしない (issue #94)。配置先が PATH 上にあるかも検査する (issue #38)。
INSTALL_DIR ?= $(HOME)/.local/bin

install:
	@mkdir -p "$(INSTALL_DIR)"
	GOBIN="$(INSTALL_DIR)" go install $(BUILD_FLAGS) ./cmd/agenthubctl
	@echo "installed $(BINARY) -> $(INSTALL_DIR)/$(BINARY)"; \
	case ":$$PATH:" in \
	  *":$(INSTALL_DIR):"*) echo "PATH OK: $(INSTALL_DIR) is on PATH";; \
	  *) echo "WARNING: $(INSTALL_DIR) is NOT on PATH."; \
	     echo "         Add it so 'agenthubctl' resolves uniquely:"; \
	     echo "         export PATH=\"$(INSTALL_DIR):\$$PATH\"";; \
	esac
	@echo "verify with: which -a agenthubctl && agenthubctl version"

clean:
	rm -f $(BINARY)
