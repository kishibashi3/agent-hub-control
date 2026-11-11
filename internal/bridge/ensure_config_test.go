package bridge

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// isolateConfigAndState は bridge config / state / log を tempdir に向ける (実 ~/.config・~/.agent-hub を汚さない)。
func isolateConfigAndState(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_HUB_HOME", t.TempDir())
	t.Setenv(state.BridgeLogDirEnv, "")
}

// TestEnsureConfigKeepsExisting: 既存 config は 1 byte も書き換えない (issue #90 完了条件「既存 31 体の config 差分 0」)。
func TestEnsureConfigKeepsExisting(t *testing.T) {
	isolateConfigAndState(t)
	orig := &bridgecfg.BridgeConfig{Handle: "keep", Workdir: "/orig", Tenant: "t1", DisplayName: "Orig", Model: "m-orig"}
	if err := bridgecfg.Save(orig); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "agenthubctl", "bridges", "keep.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	created, err := ensureConfig(bridgecfg.BridgeConfig{Handle: "keep", Workdir: "/other", Model: "m-other"})
	if err != nil || created {
		t.Fatalf("ensureConfig on existing = (%v, %v), want (false, nil)", created, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("existing config was modified:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestEnsureConfigCreates: config が無ければ作る。type は default でも明示して保存する (既存運用 config と同じ形)。
func TestEnsureConfigCreates(t *testing.T) {
	isolateConfigAndState(t)
	created, err := ensureConfig(bridgecfg.BridgeConfig{Handle: "new", Workdir: "/w", Tenant: "t", BridgeType: defaultBridgeType, DisplayName: "D", Model: "m"})
	if err != nil || !created {
		t.Fatalf("ensureConfig = (%v, %v), want (true, nil)", created, err)
	}
	got, err := bridgecfg.Load("new")
	if err != nil || got == nil {
		t.Fatalf("load: %v %v", got, err)
	}
	want := bridgecfg.BridgeConfig{Handle: "new", Workdir: "/w", Tenant: "t", BridgeType: defaultBridgeType, DisplayName: "D", Model: "m"}
	if *got != want {
		t.Errorf("config = %+v, want %+v", *got, want)
	}

	// 非 default type は保存される。type 不明 (空) は default を明示して保存する。
	for h, in := range map[string]string{"codex": "bridge-codex2", "untyped": ""} {
		if _, err := ensureConfig(bridgecfg.BridgeConfig{Handle: h, Workdir: "/w", BridgeType: in}); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := bridgecfg.Load("codex"); c == nil || c.BridgeType != "bridge-codex2" {
		t.Errorf("codex config = %+v, want BridgeType=bridge-codex2", c)
	}
	if c, _ := bridgecfg.Load("untyped"); c == nil || c.BridgeType != defaultBridgeType {
		t.Errorf("untyped config = %+v, want BridgeType=%s", c, defaultBridgeType)
	}
}

// TestEnsureConfigRejectsEmptyWorkdir: workdir 不明の config は start が cwd で起動してしまうので作らない。
func TestEnsureConfigRejectsEmptyWorkdir(t *testing.T) {
	isolateConfigAndState(t)
	if created, err := ensureConfig(bridgecfg.BridgeConfig{Handle: "nowd"}); err == nil || created {
		t.Fatalf("ensureConfig with empty workdir = (%v, %v), want error", created, err)
	}
	if c, _ := bridgecfg.Load("nowd"); c != nil {
		t.Errorf("config was written despite empty workdir: %+v", c)
	}
}

func TestParseDisplayName(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"bridge-claude2", "--participant", "a", "-display-name", "Coder — x"}, "Coder — x"},
		{[]string{"bridge-claude2", "--display-name", "D"}, "D"},
		{[]string{"bridge-claude2", "--participant", "a"}, ""},
		{[]string{"bridge-claude2", "-display-name"}, ""},
	}
	for _, tc := range cases {
		if got := parseDisplayName(tc.args); got != tc.want {
			t.Errorf("parseDisplayName(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// startFakeBridgeWithArgs は bridge に見える (argv[0] / exe basename = bridge-fake) ダミープロセスを
// 任意の bridge フラグ付きで起動する。
func startFakeBridgeWithArgs(t *testing.T, handle string, extra ...string) int {
	t.Helper()
	bin := copyShAs(t, "bridge-fake")
	args := append([]string{"-c", "sleep 30; :", "bridge-fake", "--participant", handle}, extra...)
	cmd := exec.Command(bin, args...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake bridge: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !bridgeArgvMatches(cmd.Process.Pid, handle) {
		time.Sleep(10 * time.Millisecond)
	}
	return cmd.Process.Pid
}

// TestSyncSavesConfigForRunningBridges: sync は orphan 取り込み時と、state 上で running だが config の無い
// entry に config を作る。既存 config は変えない。dead entry には作らない。--dry-run は何も書かない (issue #90)。
func TestSyncSavesConfigForRunningBridges(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	isolateConfigAndState(t)
	wdOrphan, wdTracked := t.TempDir(), t.TempDir()

	const orphan = "sync_cfg_orphan_90"
	const tracked = "sync_cfg_tracked_90"
	const configured = "sync_cfg_configured_90"
	const dead = "sync_cfg_dead_90"

	startFakeBridgeWithArgs(t, orphan, "--workdir", wdOrphan, "--tenant", "tn", "-display-name", "Orphan D", "-model", "m-o")
	trackedPID := startFakeBridgeWithArgs(t, tracked, "--workdir", wdTracked)
	configuredPID := startFakeBridgeWithArgs(t, configured, "--workdir", "/from-cmdline")

	st, unlock, err := state.LoadLocked()
	if err != nil {
		t.Fatal(err)
	}
	st.Set(tracked, trackedPID, "bridge-claude2", wdTracked, "", "m-t", modelSourceFlag, "/tmp/log")
	st.Set(configured, configuredPID, "bridge-claude2", "/from-cmdline", "", "", "", "/tmp/log")
	st.Set(dead, deadPID(t), "bridge-claude2", "/w-dead", "", "", "", "/tmp/log")
	if err := st.Save(); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()

	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: configured, Workdir: "/curated"}); err != nil {
		t.Fatal(err)
	}

	// dry-run は config を書かない。
	out := captureStdout(t, func() {
		if err := runSync(true); err != nil {
			t.Errorf("runSync(dry): %v", err)
		}
	})
	if !strings.Contains(out, "would save bridge config for running @"+orphan) || !strings.Contains(out, "would save bridge config for running @"+tracked) {
		t.Errorf("dry-run output missing 'would save bridge config' lines:\n%s", out)
	}
	for _, h := range []string{orphan, tracked} {
		if c, _ := bridgecfg.Load(h); c != nil {
			t.Errorf("dry-run wrote config for @%s: %+v", h, c)
		}
	}

	captureStdout(t, func() {
		if err := runSync(false); err != nil {
			t.Errorf("runSync: %v", err)
		}
	})

	if c, _ := bridgecfg.Load(orphan); c == nil {
		t.Errorf("sync did not save config for adopted orphan @%s", orphan)
	} else if want := (bridgecfg.BridgeConfig{Handle: orphan, Workdir: wdOrphan, Tenant: "tn", BridgeType: "bridge-fake", DisplayName: "Orphan D", Model: "m-o"}); *c != want {
		t.Errorf("orphan config = %+v, want %+v", *c, want)
	}
	if c, _ := bridgecfg.Load(tracked); c == nil {
		t.Errorf("sync did not save config for tracked running @%s", tracked)
	} else if c.Workdir != wdTracked || c.Model != "m-t" {
		t.Errorf("tracked config = %+v, want Workdir=%s Model=m-t", *c, wdTracked)
	}
	if c, _ := bridgecfg.Load(configured); c == nil || c.Workdir != "/curated" {
		t.Errorf("existing config for @%s was changed: %+v", configured, c)
	}
	if c, _ := bridgecfg.Load(dead); c != nil {
		t.Errorf("sync saved config for dead @%s: %+v", dead, c)
	}
}

// captureStderr は f 実行中の os.Stderr を取り込む。
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stderr = orig }()
	f()
	w.Close()
	return <-done
}

// TestListWarnsRunningWithoutConfig: 稼働中で config の無い handle を list が警告で列挙する (issue #90)。
func TestListWarnsRunningWithoutConfig(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	isolateConfigAndState(t)
	const noCfg = "list_nocfg_90"
	const withCfg = "list_withcfg_90"
	pidNo := startFakeBridgeWithArgs(t, noCfg, "--workdir", "/w")
	pidWith := startFakeBridgeWithArgs(t, withCfg, "--workdir", "/w")

	st, unlock, err := state.LoadLocked()
	if err != nil {
		t.Fatal(err)
	}
	st.Set(noCfg, pidNo, "bridge-claude2", "/w", "", "", "", "/tmp/log")
	st.Set(withCfg, pidWith, "bridge-claude2", "/w", "", "", "", "/tmp/log")
	if err := st.Save(); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	if err := bridgecfg.Save(&bridgecfg.BridgeConfig{Handle: withCfg, Workdir: "/w"}); err != nil {
		t.Fatal(err)
	}

	var errOut string
	captureStdout(t, func() {
		errOut = captureStderr(t, func() {
			if err := runList(); err != nil {
				t.Errorf("runList: %v", err)
			}
		})
	})
	var line string
	for _, l := range strings.Split(errOut, "\n") {
		if strings.Contains(l, "no saved config") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("list did not warn about running bridge without config; stderr:\n%s", errOut)
	}
	if !strings.Contains(line, "@"+noCfg) {
		t.Errorf("warning does not name @%s: %q", noCfg, line)
	}
	if strings.Contains(line, "@"+withCfg) {
		t.Errorf("warning names configured @%s: %q", withCfg, line)
	}
}
