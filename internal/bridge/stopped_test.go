package bridge

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
)

// isolateConfigAndState は state と bridge config を tempdir に向ける (実 config を汚さない)。
func isolateConfigAndState(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_HUB_HOME", t.TempDir())
}

// TestRunStopMarksOfflineConfig: process が無くても config があれば stopped_at を記録して成功する (issue #98)。
func TestRunStopMarksOfflineConfig(t *testing.T) {
	isolateConfigAndState(t)
	handle := "__stopped_at_offline_test_7c1e__"
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: handle, Workdir: "/nonexistent"}); err != nil {
		t.Fatal(err)
	}
	if err := runStop(handle, time.Second); err != nil {
		t.Fatalf("runStop on offline configured handle: %v", err)
	}
	cfg, _ := bridgecfg.Load(handle)
	if !cfg.Stopped() {
		t.Fatalf("stopped_at not recorded: %+v", cfg)
	}
	if _, err := time.Parse(time.RFC3339, cfg.StoppedAt); err != nil || !strings.HasSuffix(cfg.StoppedAt, "Z") {
		t.Errorf("StoppedAt %q is not RFC 3339 UTC: %v", cfg.StoppedAt, err)
	}
}

// TestRunStopNoConfigStillErrors: config も process も無い handle は従来どおりエラーで、config は作らない。
func TestRunStopNoConfigStillErrors(t *testing.T) {
	isolateConfigAndState(t)
	handle := "__stopped_at_noconfig_test_2b9d__"
	if err := runStop(handle, time.Second); err == nil {
		t.Fatal("runStop on unknown handle should fail")
	}
	if cfg, _ := bridgecfg.Load(handle); cfg != nil {
		t.Errorf("runStop must not create a config, got %+v", cfg)
	}
}

// TestRunStopMarksRunningBridge: 稼働中の bridge を stop すると stopped_at が付く。
func TestRunStopMarksRunningBridge(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	isolateConfigAndState(t)
	handle := "__stopped_at_running_test_5a0f__"
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: handle, Workdir: "/nonexistent"}); err != nil {
		t.Fatal(err)
	}
	startTermDelayProc(t, handle, "0")
	if err := runStop(handle, 5*time.Second); err != nil {
		t.Fatalf("runStop: %v", err)
	}
	if cfg, _ := bridgecfg.Load(handle); !cfg.Stopped() {
		t.Errorf("stopped_at not recorded after stopping a running bridge: %+v", cfg)
	}
}

// TestStartAllSkipsStopped: start --all は stopped_at のある config を起こさない。workdir が存在しないので
// spawn を試みればエラーになる = nil が返れば skip されている。
func TestStartAllSkipsStopped(t *testing.T) {
	isolateConfigAndState(t)
	handle := "__stopped_at_startall_test_e4d2__"
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: handle, Workdir: "/nonexistent", StoppedAt: "2026-11-16T00:00:00.000Z"}); err != nil {
		t.Fatal(err)
	}
	if err := runStartAll(1); err != nil {
		t.Fatalf("start --all should skip the stopped bridge, got: %v", err)
	}

	// stopped_at を消すと起こしにいく (= spawn を試みて workdir エラー)。
	if err := clearStoppedForExplicitStart(handle); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := bridgecfg.Load(handle); cfg.Stopped() {
		t.Fatalf("clearStoppedForExplicitStart did not clear: %+v", cfg)
	}
	if err := runStartAll(1); err == nil {
		t.Fatal("start --all should try to spawn the un-stopped bridge (and fail on the missing workdir)")
	}
}

func TestConfigStateDisplay(t *testing.T) {
	if got := configStateDisplay(&bridgecfg.BridgeConfig{Handle: "a"}); got != "run" {
		t.Errorf("no stopped_at = %q, want run", got)
	}
	if got := configStateDisplay(&bridgecfg.BridgeConfig{Handle: "a", StoppedAt: "2026-11-16T00:00:00.000Z"}); got != "stopped since 2026-11-16T00:00:00.000Z" {
		t.Errorf("stopped = %q", got)
	}
}
