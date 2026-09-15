package state_test

import (
	"path/filepath"
	"testing"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// TestBridgeLogPath: 解決順 AGENT_HUB_BRIDGE_LOG_DIR > $AGENT_HUB_HOME/logs (issue #54)。
func TestBridgeLogPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENT_HUB_HOME", home)
	t.Setenv(state.BridgeLogDirEnv, "")

	got, err := state.BridgeLogPath("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "logs", "bridge-alpha.out.log"); got != want {
		t.Errorf("AGENT_HUB_HOME: got %q, want %q", got, want)
	}

	override := t.TempDir()
	t.Setenv(state.BridgeLogDirEnv, override)
	got, err = state.BridgeLogPath("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(override, "bridge-alpha.out.log"); got != want {
		t.Errorf("%s: got %q, want %q", state.BridgeLogDirEnv, got, want)
	}

	t.Setenv(state.BridgeLogDirEnv, "relative/logs")
	if _, err := state.BridgeLogPath("alpha"); err == nil {
		t.Errorf("relative %s must be rejected (dangling legacy symlink / cwd-dependent log_path)", state.BridgeLogDirEnv)
	}
	t.Setenv(state.BridgeLogDirEnv, "")

	if got := state.LegacyBridgeLogPath("/tmp", "alpha"); got != "/tmp/bridge-alpha.log" {
		t.Errorf("legacy: got %q", got)
	}
}
