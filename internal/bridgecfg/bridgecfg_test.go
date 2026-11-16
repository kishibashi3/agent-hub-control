package bridgecfg

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSaveLoadRoundTripModel: model は保存・復元され、未設定なら JSON に出ない (omitempty、旧ファイル互換)。
func TestSaveLoadRoundTripModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := Save(&BridgeConfig{Handle: "alpha", Workdir: "/w", Model: "some-model-id"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load("alpha")
	if err != nil || got == nil {
		t.Fatalf("load: %v (%+v)", err, got)
	}
	if got.Model != "some-model-id" || got.Workdir != "/w" {
		t.Errorf("round-trip = %+v, want Model=some-model-id Workdir=/w", got)
	}

	if err := Save(&BridgeConfig{Handle: "beta", Workdir: "/w"}); err != nil {
		t.Fatalf("save beta: %v", err)
	}
	if got, err := Load("beta"); err != nil || got == nil || got.Model != "" {
		t.Errorf("unset model should load as empty, got %+v (%v)", got, err)
	}
}

// TestMarkClearStopped: stop の記録 / start での消去 (issue #98)。
func TestMarkClearStopped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// config が無ければ作らない。
	if marked, err := MarkStopped("ghost", time.Now()); err != nil || marked {
		t.Fatalf("MarkStopped(no config) = %v, %v; want false, nil", marked, err)
	}
	if cfg, _ := Load("ghost"); cfg != nil {
		t.Fatalf("MarkStopped must not create a config, got %+v", cfg)
	}

	if err := Save(&BridgeConfig{Handle: "alpha", Workdir: "/w", Model: "m"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	t1 := time.Date(2026, 11, 16, 3, 4, 5, 6_000_000, time.FixedZone("JST", 9*3600))
	if marked, err := MarkStopped("alpha", t1); err != nil || !marked {
		t.Fatalf("MarkStopped = %v, %v; want true, nil", marked, err)
	}
	got, _ := Load("alpha")
	if got.StoppedAt != "2026-11-15T18:04:05.006Z" || !got.Stopped() {
		t.Errorf("StoppedAt = %q, want 2026-11-15T18:04:05.006Z (UTC, Z suffix)", got.StoppedAt)
	}
	if got.Workdir != "/w" || got.Model != "m" {
		t.Errorf("other fields changed: %+v", got)
	}

	// 2 回目の stop は元の時刻を残す (いつから止めてあるか)。
	if _, err := MarkStopped("alpha", t1.Add(time.Hour)); err != nil {
		t.Fatalf("MarkStopped again: %v", err)
	}
	if got, _ := Load("alpha"); got.StoppedAt != "2026-11-15T18:04:05.006Z" {
		t.Errorf("second stop overwrote StoppedAt: %q", got.StoppedAt)
	}

	if cleared, err := ClearStopped("alpha"); err != nil || !cleared {
		t.Fatalf("ClearStopped = %v, %v; want true, nil", cleared, err)
	}
	got, _ = Load("alpha")
	if got.Stopped() || got.Workdir != "/w" || got.Model != "m" {
		t.Errorf("after clear = %+v, want running with other fields kept", got)
	}
	path, _ := configPath("alpha")
	if data, _ := os.ReadFile(path); strings.Contains(string(data), "stopped_at") {
		t.Errorf("cleared config still has stopped_at key: %s", data)
	}
}

// TestClearStoppedLeavesUnstoppedFileUntouched: stopped_at の無い既存 config はバイト単位で書き換えない
// (既存 config の sha256 差分 0、issue #98)。
func TestClearStoppedLeavesUnstoppedFileUntouched(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, _ := configPath("legacy")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// 手書き風 (MarshalIndent と違う整形) の旧形式ファイル。
	orig := []byte(`{"handle":"legacy","workdir":"/w"}` + "\n")
	if err := os.WriteFile(path, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	if cleared, err := ClearStopped("legacy"); err != nil || cleared {
		t.Fatalf("ClearStopped = %v, %v; want false, nil", cleared, err)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, orig) {
		t.Errorf("file rewritten: %q", data)
	}
	if cfg, _ := Load("legacy"); cfg.Stopped() {
		t.Errorf("legacy config without stopped_at must read as run")
	}
}
