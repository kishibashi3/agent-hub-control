VERSION ?= dev
BINARY  := agenthubctl
BUILD_FLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build install clean

build:
	go build $(BUILD_FLAGS) -o $(BINARY) ./cmd/agenthubctl

# install は ~/.local/bin (INSTALL_DIR) に配置する。GOBIN/GOPATH や mise の Go install dir は
# Go の設定・版で場所が変わるため入れ先にしない (issue #94)。配置先が PATH 上にあるかも検査する (issue #38)。
INSTALL_DIR ?= $(HOME)/.local/bin
# 末尾の / を落とし、PATH 検査の比較で誤った WARNING を出さないようにする (issue #99)。
INSTALL_DEST := $(patsubst %/,%,$(INSTALL_DIR))

# PATH 検査は 2 段 (issue #99):
#   1. 入れ先が PATH 上にあるか
#   2. shell が最初に選ぶ agenthubctl (command -v) が入れ先の copy か。PATH の前のほうに
#      古い copy があると 1 だけでは気づけない (#94)。削除も PATH の並べ替えもせず、警告だけ出す。
#      比較は -ef (同一 file か) で行い、PATH 側の末尾 / や symlink 経由の dir で誤検知しないようにする。
# 相対 path や ~ は go install が拒否するが、mkdir -p が先に走ると空 dir が残るので先に弾く。
install:
	@case "$(INSTALL_DEST)" in \
	  /*) ;; \
	  *) echo "ERROR: INSTALL_DIR must be an absolute path (got '$(INSTALL_DIR)')." >&2; exit 1;; \
	esac
	@mkdir -p "$(INSTALL_DEST)"
	GOBIN="$(INSTALL_DEST)" go install $(BUILD_FLAGS) ./cmd/agenthubctl
	@echo "installed $(BINARY) -> $(INSTALL_DEST)/$(BINARY)"; \
	case ":$$PATH:" in \
	  *":$(INSTALL_DEST):"*|*":$(INSTALL_DEST)/:"*) ON_PATH=1;; \
	  *) ON_PATH=0; \
	     echo "WARNING: $(INSTALL_DEST) is NOT on PATH."; \
	     echo "         Add it so 'agenthubctl' resolves uniquely:"; \
	     echo "         export PATH=\"$(INSTALL_DEST):\$$PATH\"";; \
	esac; \
	FIRST="$$(command -v $(BINARY) 2>/dev/null || true)"; \
	if [ -n "$$FIRST" ] && ! [ "$$FIRST" -ef "$(INSTALL_DEST)/$(BINARY)" ]; then \
	  echo "WARNING: '$(BINARY)' resolves to $$FIRST, not $(INSTALL_DEST)/$(BINARY)."; \
	  echo "         Another copy comes earlier on PATH. Remove it or put $(INSTALL_DEST) first."; \
	elif [ "$$ON_PATH" = 1 ]; then \
	  echo "PATH OK: $(INSTALL_DEST) is on PATH and '$(BINARY)' resolves to it"; \
	fi
	@echo "verify with: which -a agenthubctl && agenthubctl version"

clean:
	rm -f $(BINARY)
