package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// setupBinDir は AGENT_HUB_HOME / XDG_CONFIG_HOME を tempdir に向け、移行期間の env を消し、$BIN を作って返す
// (本番の ~/.agent-hub/bin を使わない、issue #88)。
func setupBinDir(t *testing.T) string {
	t.Helper()
	isolateConfigAndState(t)
	for _, bt := range []string{"bridge-claude2", "bridge-codex2", "bridge-tmux"} {
		t.Setenv(binEnvVar(bt), "")
	}
	dir, err := state.BinDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeExecFile(t *testing.T, dir, name string, perm os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), perm); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func symlinkOrFatal(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// TestResolveBinaryDefaultSymlink: 何も指定しなければ $BIN/<type> (deploy が張る symlink) を使い、実体を返す。
func TestResolveBinaryDefaultSymlink(t *testing.T) {
	dir := setupBinDir(t)
	real := writeExecFile(t, dir, "bridge-claude2@aaaa", 0o755)
	symlinkOrFatal(t, "bridge-claude2@aaaa", filepath.Join(dir, "bridge-claude2"))

	got, err := resolveBinary("bridge-claude2", binSpec{})
	if err != nil {
		t.Fatal(err)
	}
	want := resolvedBinary{Path: filepath.Join(dir, "bridge-claude2"), Real: real, Source: binSourceDefault}
	if got != want {
		t.Errorf("resolveBinary = %+v, want %+v", got, want)
	}
}

// TestResolveBinaryNoPATHFallback: $BIN/<type> が無ければ、PATH に同名の binary があってもエラーにする (#74 の目的)。
// エラーには試した場所と直し方を出す。
func TestResolveBinaryNoPATHFallback(t *testing.T) {
	dir := setupBinDir(t)
	pathDir := t.TempDir()
	writeExecFile(t, pathDir, "bridge-claude2", 0o755)
	t.Setenv("PATH", pathDir)

	_, err := resolveBinary("bridge-claude2", binSpec{})
	if err == nil {
		t.Fatal("resolveBinary fell back to PATH; want error")
	}
	for _, s := range []string{filepath.Join(dir, "bridge-claude2"), "AGENT_HUB_BRIDGE_CLAUDE2_BIN", "PATH is not searched", "make deploy --activate", "--bin"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not mention %q", err, s)
		}
	}
}

// TestResolveBinaryPinByName: --bin / config の bin は $BIN の中のファイル名で、<type>@<revision> に揃っていない
// 名前も指せる。固定指定は env より優先する。
func TestResolveBinaryPinByName(t *testing.T) {
	dir := setupBinDir(t)
	writeExecFile(t, dir, "bridge-claude2@aaaa", 0o755)
	symlinkOrFatal(t, "bridge-claude2@aaaa", filepath.Join(dir, "bridge-claude2"))
	pinned := writeExecFile(t, dir, "bridge-claude2@bbbb", 0o755)
	odd := writeExecFile(t, dir, "bridge-claude2-344", 0o755)
	t.Setenv("AGENT_HUB_BRIDGE_CLAUDE2_BIN", writeExecFile(t, t.TempDir(), "bridge-claude2", 0o755))

	for _, c := range []struct {
		pin  binSpec
		want string
	}{
		{binSpec{Name: "bridge-claude2@bbbb", Source: binSourceConfig}, pinned},
		{binSpec{Name: "bridge-claude2-344", Source: binSourceFlag}, odd},
	} {
		got, err := resolveBinary("bridge-claude2", c.pin)
		if err != nil {
			t.Fatalf("pin %+v: %v", c.pin, err)
		}
		want := resolvedBinary{Path: c.want, Real: c.want, Name: c.pin.Name, Source: c.pin.Source}
		if got != want {
			t.Errorf("pin %+v: resolveBinary = %+v, want %+v", c.pin, got, want)
		}
	}
}

// TestResolveBinaryRejectsPathValues: --bin / config の bin に "/" を含む値 (フルパス・"..") は受け付けない。
func TestResolveBinaryRejectsPathValues(t *testing.T) {
	dir := setupBinDir(t)
	outside := writeExecFile(t, t.TempDir(), "bridge-claude2", 0o755)
	writeExecFile(t, dir, "bridge-claude2", 0o755)
	for _, name := range []string{outside, "../bin/bridge-claude2", "sub/bridge-claude2", "..", ".", "~bridge", "bridge-$X"} {
		if _, err := resolveBinary("bridge-claude2", binSpec{Name: name, Source: binSourceConfig}); err == nil {
			t.Errorf("bin %q accepted; want error", name)
		}
		if err := validateBinName(name); err == nil {
			t.Errorf("validateBinName(%q) = nil; want error", name)
		}
	}
	if _, err := resolveBinary("bridge-claude2", binSpec{Name: "bridge-claude2@missing", Source: binSourceConfig}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing pinned file: err = %v, want not found", err)
	}
}

// TestResolveBinaryChecksRealTarget: 検査は symlink を辿った先の実体にかける (2026-10-18 operator 付記)。
func TestResolveBinaryChecksRealTarget(t *testing.T) {
	dir := setupBinDir(t)
	other := t.TempDir()
	nonExec := writeExecFile(t, other, "bridge-claude2@noexec", 0o644)
	notBridge := writeExecFile(t, other, "claude2-wrapper", 0o755)
	subdir := filepath.Join(other, "bridge-claude2@dir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	dollarDir := filepath.Join(other, "$HOME")
	if err := os.Mkdir(dollarDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dollar := writeExecFile(t, dollarDir, "bridge-claude2@d", 0o755)

	cases := []struct {
		name, target, want string
	}{
		{"bridge-claude2@l-noexec", nonExec, "not executable"},
		{"bridge-claude2@l-wrapper", notBridge, "symlink target"},
		{"bridge-claude2@l-dir", subdir, "not a regular file"},
		{"bridge-claude2@l-dangling", filepath.Join(other, "gone"), "not found"},
		{"bridge-claude2@l-dollar", dollar, "'~' or '$'"},
	}
	for _, c := range cases {
		symlinkOrFatal(t, c.target, filepath.Join(dir, c.name))
		_, err := resolveBinary("bridge-claude2", binSpec{Name: c.name, Source: binSourceConfig})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s -> %s: err = %v, want %q", c.name, c.target, err, c.want)
		}
	}
	// 既定の symlink も同じ検査を通る。
	symlinkOrFatal(t, nonExec, filepath.Join(dir, "bridge-claude2"))
	if _, err := resolveBinary("bridge-claude2", binSpec{}); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Errorf("default symlink to non-executable: err = %v", err)
	}
}

// TestResolveBinaryEnvDeprecated: 移行期間は env を既定より優先して読み、#82 の検査をかける。
func TestResolveBinaryEnvDeprecated(t *testing.T) {
	dir := setupBinDir(t)
	writeExecFile(t, dir, "bridge-claude2", 0o755)
	envBin := writeExecFile(t, t.TempDir(), "bridge-claude2", 0o755)

	t.Setenv("AGENT_HUB_BRIDGE_CLAUDE2_BIN", envBin)
	got, err := resolveBinary("bridge-claude2", binSpec{})
	if err != nil || got.Path != envBin || got.Source != binSourceEnv || got.Name != "" {
		t.Errorf("resolveBinary = (%+v, %v), want env %s", got, err, envBin)
	}

	for _, c := range []struct{ val, want string }{
		{"~/bin/bridge-claude2", "'~' or '$'"},
		{"$HOME/bin/bridge-claude2", "'~' or '$'"},
		{"bin/bridge-claude2", "relative path"},
		{filepath.Join(dir, "bridge-claude2@nope"), "not found"},
		{writeExecFile(t, t.TempDir(), "claude2-wrapper", 0o755), "bridge-"},
	} {
		t.Setenv("AGENT_HUB_BRIDGE_CLAUDE2_BIN", c.val)
		if _, err := resolveBinary("bridge-claude2", binSpec{}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("env %q: err = %v, want %q", c.val, err, c.want)
		}
	}
}

// TestPinFromConfigOrState: restart / watchdog は config の bin > state の前回値 > 固定なし。
// state の値が config 由来で、いまの config の bin が空なら固定しない (PR #104 review 指摘 1)。
func TestPinFromConfigOrState(t *testing.T) {
	flagPin := &state.Entry{Bin: "bridge-claude2@state", BinSource: binSourceFlag}
	cfgPin := &state.Entry{Bin: "bridge-claude2@state", BinSource: binSourceConfig}
	stateBin := binSpec{Name: "bridge-claude2@state", Source: binSourceState}
	for _, c := range []struct {
		name  string
		entry *state.Entry
		cfg   *bridgecfg.BridgeConfig
		want  binSpec
	}{
		{"config bin wins", flagPin, &bridgecfg.BridgeConfig{Bin: "bridge-claude2@cfg"}, binSpec{Name: "bridge-claude2@cfg", Source: binSourceConfig}},
		{"no config, flag pin", flagPin, nil, stateBin},
		{"no config, config pin", cfgPin, nil, stateBin},
		{"empty config bin, flag pin", flagPin, &bridgecfg.BridgeConfig{}, stateBin},
		{"empty config bin, config pin (unpinned)", cfgPin, &bridgecfg.BridgeConfig{}, binSpec{}},
		{"nothing", &state.Entry{}, nil, binSpec{}},
	} {
		if got := pinFromConfigOrState(c.entry, c.cfg); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestRespawnBinReadsConfig: watchdog の respawn は bridge config の bin を読む (flag だけの固定が
// respawn で既定の版に戻らない)。
func TestRespawnBinReadsConfig(t *testing.T) {
	isolateConfigAndState(t)
	e := state.Entry{Handle: "__bin_respawn_test__", Bin: "bridge-claude2@state"}
	if got := respawnBin(e); got != (binSpec{Name: "bridge-claude2@state", Source: binSourceState}) {
		t.Errorf("no config: %+v", got)
	}
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: e.Handle, Bin: "bridge-claude2@cfg"}); err != nil {
		t.Fatal(err)
	}
	if got := respawnBin(e); got != (binSpec{Name: "bridge-claude2@cfg", Source: binSourceConfig}) {
		t.Errorf("with config: %+v", got)
	}
	// config set --bin "" の後: config 由来の固定は外れる (PR #104 review 指摘 1)
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: e.Handle}); err != nil {
		t.Fatal(err)
	}
	e.BinSource = binSourceConfig
	if got := respawnBin(e); got != (binSpec{}) {
		t.Errorf("config bin cleared: %+v", got)
	}
}

// TestSpawnBinSpec: spawn は --bin (空なら固定なし) > config の bin。--bin "" と未指定を区別する。
func TestSpawnBinSpec(t *testing.T) {
	cfg := &bridgecfg.BridgeConfig{Bin: "bridge-claude2@cfg"}
	for _, c := range []struct {
		name    string
		flagSet bool
		bin     string
		cfg     *bridgecfg.BridgeConfig
		want    binSpec
	}{
		{"flag", true, "bridge-claude2@f", cfg, binSpec{Name: "bridge-claude2@f", Source: binSourceFlag}},
		{"flag empty overrides config", true, "", cfg, binSpec{}},
		{"unset uses config", false, "", cfg, binSpec{Name: "bridge-claude2@cfg", Source: binSourceConfig}},
		{"unset, no config", false, "", nil, binSpec{}},
	} {
		got, err := spawnBinSpec(c.flagSet, c.bin, c.cfg)
		if err != nil || got != c.want {
			t.Errorf("%s: %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
	if _, err := spawnBinSpec(true, "../bridge-claude2", nil); err == nil {
		t.Error("path in --bin: want error")
	}
}

// TestConfigGetListShowBin: config get / list は bin を表示し、未設定は (default)。
func TestConfigGetListShowBin(t *testing.T) {
	isolateConfigAndState(t)
	for _, c := range []*bridgecfg.BridgeConfig{{Handle: "__bin_show_a__", Bin: "bridge-claude2@aaa"}, {Handle: "__bin_show_b__"}} {
		if err := bridgecfg.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	get := captureStdout(t, func() {
		cmd := newConfigGetCmd()
		cmd.SetArgs([]string{"__bin_show_a__"})
		if err := cmd.Execute(); err != nil {
			t.Error(err)
		}
	})
	if !regexp.MustCompile(`(?m)^bin\s+bridge-claude2@aaa$`).MatchString(get) {
		t.Errorf("get: %q", get)
	}
	list := captureStdout(t, func() {
		cmd := newConfigListCmd()
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(list, "bridge-claude2@aaa") || !strings.Contains(list, "(default)") {
		t.Errorf("list: %q", list)
	}
}

// TestRunRestartPassesBin: restart は config の bin、無ければ state の前回値で re-spawn する。
// `config set --bin ""` → restart で config 由来の固定が外れる (PR #104 review 指摘 1)。
func TestRunRestartPassesBin(t *testing.T) {
	stateBin := binSpec{Name: "bridge-claude2@state", Source: binSourceState}
	for _, c := range []struct {
		name     string
		stateSrc string
		cfg      *bridgecfg.BridgeConfig
		want     binSpec
	}{
		{"no config", binSourceFlag, nil, stateBin},
		{"config bin", binSourceFlag, &bridgecfg.BridgeConfig{Bin: "bridge-claude2@cfg"}, binSpec{Name: "bridge-claude2@cfg", Source: binSourceConfig}},
		{"config without bin, flag pin", binSourceFlag, &bridgecfg.BridgeConfig{}, stateBin},
		{"config bin cleared, config pin", binSourceConfig, &bridgecfg.BridgeConfig{}, binSpec{}},
	} {
		handle := "__bin_restart_test__"
		setupRestartEntry(t, handle, deadPID(t))
		st, unlock, err := state.LoadLocked()
		if err != nil {
			t.Fatal(err)
		}
		st.Get(handle).Bin = "bridge-claude2@state"
		st.Get(handle).BinSource = c.stateSrc
		if err := st.Save(); err != nil {
			t.Fatal(err)
		}
		unlock()
		if c.cfg != nil {
			c.cfg.Handle = handle
			if err := bridgecfg.Save(c.cfg); err != nil {
				t.Fatal(err)
			}
		}

		var got binSpec
		orig := restartSpawn
		restartSpawn = func(_, _, _, _, _ string, _ modelSpec, bin binSpec, _ int) error {
			got = bin
			return nil
		}
		err = runRestart(handle, "", 1, time.Second)
		restartSpawn = orig
		if err != nil {
			t.Fatalf("%s: runRestart: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: re-spawn bin = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestConfigSetBin: config set --bin はファイル名だけを受け付け、--bin "" で固定を外す。
func TestConfigSetBin(t *testing.T) {
	setupBinDir(t)
	handle := "__bin_config_set_test__"
	run := func(args ...string) error {
		cmd := newConfigSetCmd()
		cmd.SetArgs(append([]string{handle}, args...))
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		return cmd.Execute()
	}
	if err := run("--bin", "/home/x/.agent-hub/bin/bridge-claude2@aaaa"); err == nil {
		t.Error("config set --bin <full path> accepted; want error")
	}
	if err := run("--bin", "bridge-claude2@aaaa"); err != nil {
		t.Fatalf("config set --bin <name>: %v", err)
	}
	if cfg, _ := bridgecfg.Load(handle); cfg == nil || cfg.Bin != "bridge-claude2@aaaa" {
		t.Errorf("config = %+v, want bin bridge-claude2@aaaa", cfg)
	}
	if err := run("--bin", ""); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := bridgecfg.Load(handle); cfg == nil || cfg.Bin != "" {
		t.Errorf("config = %+v, want bin cleared", cfg)
	}
}

// TestRunSpawnRecordsPinnedBin: 固定指定で spawn すると state に名前と出所が残り、その binary が起動される。
func TestRunSpawnRecordsPinnedBin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	dir := setupBinDir(t)
	_, thisFile, _, _ := runtime.Caller(0)
	script, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "testdata", "fake-bridge-args.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bridge-claude2@pinned"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(state.BridgeLogDirEnv, "")
	prevLegacy := legacyLogDir
	legacyLogDir = t.TempDir()
	t.Cleanup(func() { legacyLogDir = prevLegacy })

	const handle = "__test_spawn_pinned_bin__"
	pkillHandle(handle)
	t.Cleanup(func() { pkillHandle(handle) })

	if err := runSpawn(handle, "bridge-claude2", t.TempDir(), "", "", modelSpec{}, binSpec{Name: "bridge-claude2@pinned", Source: binSourceFlag}, 10, stoppedIgnore); err != nil {
		t.Fatalf("runSpawn: %v", err)
	}
	st, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if e := st.Get(handle); e == nil || e.Bin != "bridge-claude2@pinned" || e.BinSource != binSourceFlag {
		t.Errorf("state entry = %+v, want Bin=bridge-claude2@pinned BinSource=flag", e)
	}
}

// TestExeSHACache: status の EXE / SHA は /proc/<pid>/exe から読み、ファイルが消された後も実際に動いている
// 中身の sha を返す (issue #88)。
func TestExeSHACache(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/exe")
	}
	bin := copyShAs(t, "bridge-fake")
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	wantSHA := hex.EncodeToString(sum[:])

	cmd := exec.Command(bin, "-c", "sleep 30; :")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if exe, _ := state.ReadExe(pid); exe == bin {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	c := exeSHACache{}
	exe, sha := c.info(pid)
	if exe != bin || sha != wantSHA {
		t.Errorf("info = (%q, %q), want (%q, %q)", exe, sha, bin, wantSHA)
	}
	if len(c) != 1 {
		t.Errorf("cache size = %d, want 1", len(c))
	}

	// 差し替え (削除) 後も、動いている中身の sha を出し、exe には kernel の " (deleted)" を残す。
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	exe, sha = exeSHACache{}.info(pid)
	if exe != bin+" (deleted)" || sha != wantSHA {
		t.Errorf("after delete: info = (%q, %q), want (%q, %q)", exe, sha, bin+" (deleted)", wantSHA)
	}

	// 読めない PID は "-"。
	if exe, sha := (exeSHACache{}).info(deadPID(t)); exe != "-" || sha != "-" {
		t.Errorf("dead pid: info = (%q, %q), want (-, -)", exe, sha)
	}
}
