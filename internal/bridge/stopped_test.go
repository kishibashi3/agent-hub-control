package bridge

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
	"github.com/kishibashi3/agent-hub-control/internal/state"
)

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

// fakeSpawnBinary は runSpawn が起動する bridge binary を /bin/sh のコピー (bridge-fake) に向ける。
// sh は --participant を知らないので即終了し、ready を待つ runSpawn は timeout で失敗する。
func fakeSpawnBinary(t *testing.T) string {
	t.Helper()
	t.Setenv("AGENT_HUB_BRIDGE_FAKE_BIN", copyShAs(t, "bridge-fake"))
	return "bridge-fake"
}

// TestRunSpawnSkipRereadsConfig: start --all 用の stoppedSkip は ListAll 時点の値でなく、state lock を
// 持ったまま config を読み直して弾く (PR #102 review Minor 1: ループ中の stop を取り消さない)。
func TestRunSpawnSkipRereadsConfig(t *testing.T) {
	isolateConfigAndState(t)
	bt := fakeSpawnBinary(t)
	handle := "__stopped_at_skip_reread_test_91a3__"
	// start --all がループに入った後に `bridge stop` された状況: 今の config は止めてある。
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: handle, Workdir: t.TempDir(), StoppedAt: "2026-11-16T00:00:00.000Z"}); err != nil {
		t.Fatal(err)
	}
	err := runSpawn(handle, bt, t.TempDir(), "", "", modelSpec{}, binSpec{}, 1, stoppedSkip)
	var se *errStoppedInConfig
	if !errors.As(err, &se) {
		t.Fatalf("runSpawn(stoppedSkip) on a stopped config = %v; want errStoppedInConfig", err)
	}
	if _, err := os.Stat(mustLogPath(t, handle)); err == nil {
		t.Error("runSpawn(stoppedSkip) must not touch the log / launch the bridge")
	}
}

// TestRunSpawnClearKeepsStoppedWhenRunning: 明示 start / spawn が「already running」で弾かれた場合は
// stopped_at を消さない (PR #102 review Minor 2: drain 中の stop と重なっても fleet が復活させない)。
func TestRunSpawnClearKeepsStoppedWhenRunning(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	isolateConfigAndState(t)
	bt := fakeSpawnBinary(t)
	handle := "__stopped_at_clear_running_test_3e58__"
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: handle, Workdir: t.TempDir(), StoppedAt: "2026-11-16T00:00:00.000Z"}); err != nil {
		t.Fatal(err)
	}
	startTermDelayProc(t, handle, "0") // まだ drain 中の bridge (pgrep に見える)
	err := runSpawn(handle, bt, t.TempDir(), "", "", modelSpec{}, binSpec{}, 1, stoppedClear)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("runSpawn = %v; want already running", err)
	}
	if cfg, _ := bridgecfg.Load(handle); !cfg.Stopped() {
		t.Errorf("stopped_at must survive a rejected start: %+v", cfg)
	}
}

// TestRunStartOneClearsStopped: 明示 start は running チェックを通れば、起動に失敗しても stopped_at を消す
// (起こす意図を残す)。PR #102 review Suggestion 3: runStartOne 経路のテスト。
func TestRunStartOneClearsStopped(t *testing.T) {
	isolateConfigAndState(t)
	bt := fakeSpawnBinary(t)
	handle := "__stopped_at_startone_test_c07b__"
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: handle, Workdir: t.TempDir(), BridgeType: bt, StoppedAt: "2026-11-16T00:00:00.000Z"}); err != nil {
		t.Fatal(err)
	}
	if err := runStartOne(handle, 1); err == nil {
		t.Fatal("fake bridge never becomes ready; runStartOne should time out")
	}
	if cfg, _ := bridgecfg.Load(handle); cfg.Stopped() {
		t.Errorf("explicit start must clear stopped_at: %+v", cfg)
	}
}

// TestSpawnCmdClearsStopped: `bridge spawn` (CLI) も stopped_at を消す。PR #102 review Suggestion 3。
func TestSpawnCmdClearsStopped(t *testing.T) {
	isolateConfigAndState(t)
	bt := fakeSpawnBinary(t)
	handle := "__stopped_at_spawncmd_test_5d16__"
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: handle, Workdir: t.TempDir(), StoppedAt: "2026-11-16T00:00:00.000Z"}); err != nil {
		t.Fatal(err)
	}
	cmd := NewSpawnCmd()
	cmd.SetArgs([]string{"-p", handle, "--type", bt, "--timeout", "1"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("fake bridge never becomes ready; spawn should time out")
	}
	if cfg, _ := bridgecfg.Load(handle); cfg.Stopped() {
		t.Errorf("bridge spawn must clear stopped_at: %+v", cfg)
	}
}

// TestRunStopStillStopsWhenMarkFails: config が壊れていて stopped_at を書けなくても SIGTERM は送り、
// 記録の失敗は最後にエラーで返す (PR #102 review Minor 4)。
func TestRunStopStillStopsWhenMarkFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	isolateConfigAndState(t)
	handle := "__stopped_at_markfail_test_a842__"
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: handle}); err != nil {
		t.Fatal(err)
	}
	path := mustConfigPath(t, handle)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, done := startTermDelayProc(t, handle, "0")
	err := runStop(handle, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "record stopped_at") {
		t.Fatalf("runStop = %v; want the stopped_at record failure", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runStop must still stop the bridge when stopped_at cannot be recorded")
	}
}

func mustLogPath(t *testing.T, handle string) string {
	t.Helper()
	p, err := state.BridgeLogPath(handle)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustConfigPath(t *testing.T, handle string) string {
	t.Helper()
	return os.Getenv("XDG_CONFIG_HOME") + "/agenthubctl/bridges/" + handle + ".json"
}
