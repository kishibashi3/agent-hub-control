package bridge

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// deadPID は起動して即終了・reap 済みの PID を返す。FindProcess は成功するが
// Signal(0) は ESRCH を返すため IsRunning() が false になる「死んだ PID」を得る。
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start throwaway proc: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait() // reap してゾンビにしない
	return pid
}

// TestRunStopCleansStaleState は、state に entry はあるが実プロセスは死んでおり、
// 同一 handle の live プロセスも存在しない場合に runStop が stale entry を掃除して
// nil を返すこと (reconcile 系の stale-state cleanup 分岐) を検証する (issue #38)。
func TestRunStopCleansStaleState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test relies on /proc semantics")
	}
	tmp := t.TempDir()
	t.Setenv("AGENT_HUB_HOME", tmp)

	// pgrep にどのプロセスとも当たらない一意な handle を使う (= live プロセス無しを保証)。
	handle := "__stale_cleanup_test_handle_9f3a__"

	st, unlock, err := state.LoadLocked()
	if err != nil {
		t.Fatalf("load locked: %v", err)
	}
	st.Set(handle, deadPID(t), "bridge-claude2", "/tmp/wd", "", "", "", "/tmp/log")
	if err := st.Save(); err != nil {
		unlock()
		t.Fatalf("save: %v", err)
	}
	unlock()

	if err := runStop(handle); err != nil {
		t.Fatalf("runStop should succeed cleaning stale state, got: %v", err)
	}

	// entry が掃除されていることを確認する。
	reloaded, err := state.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Get(handle) != nil {
		t.Errorf("expected stale entry @%s to be removed, still present", handle)
	}
}

// TestRunListReconcilesRestartedHandle は、state の記録 PID は死んでいるが同一 handle が
// 新しい PID で再起動している場合に、list がその handle を dead ではなく live PID の running
// として表示すること (issue #38 の list/stop 非対称解消) を検証する。
func TestRunListReconcilesRestartedHandle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	tmp := t.TempDir()
	t.Setenv("AGENT_HUB_HOME", tmp)

	handle := "reconcile_restart_test_handle_7c1e"

	// 同一 handle の live な bridge プロセスを起動する (= 再起動後の実プロセス相当)。
	proc := startFakeProc(t, "bridge-claude2", "--participant", handle)
	defer func() { _ = proc.Process.Kill() }()
	livePID := proc.Process.Pid

	// state には別の (死んだ) PID を記録しておく = stale entry。
	st, unlock, err := state.LoadLocked()
	if err != nil {
		t.Fatalf("load locked: %v", err)
	}
	st.Set(handle, deadPID(t), "bridge-claude2", "/tmp/wd", "", "", "", "/tmp/log")
	if err := st.Save(); err != nil {
		unlock()
		t.Fatalf("save: %v", err)
	}
	unlock()

	out := captureStdout(t, func() {
		if err := runList(); err != nil {
			t.Errorf("runList error: %v", err)
		}
	})

	// 対象 handle の行を取り出して検証する (他の実 bridge 行に惑わされないように)。
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, handle) {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("handle @%s not found in list output:\n%s", handle, out)
	}
	if !strings.Contains(line, "running") {
		t.Errorf("expected @%s to show running (reconciled), got line: %q", handle, line)
	}
	if strings.Contains(line, "dead") {
		t.Errorf("expected @%s NOT to show dead, got line: %q", handle, line)
	}
	if !strings.Contains(line, strconv.Itoa(livePID)) {
		t.Errorf("expected @%s line to contain live pid=%d, got line: %q", handle, livePID, line)
	}
}

// TestListShowsModelColumn: `bridge list` に MODEL 列があり、state の model が表示される。
// 未指定は "(default)" と断定せず "(not passed)" (bridge 側が env / default で解決するため) (PR #67 review)。
func TestListShowsModelColumn(t *testing.T) {
	t.Setenv("AGENT_HUB_HOME", t.TempDir())
	st, unlock, err := state.LoadLocked()
	if err != nil {
		t.Fatalf("load locked: %v", err)
	}
	st.Set("with-model", deadPID(t), "bridge-claude2", "/tmp/wd", "", "model-abc", "config", "/tmp/log")
	st.Set("no-model", deadPID(t), "bridge-claude2", "/tmp/wd", "", "", "", "/tmp/log")
	if err := st.Save(); err != nil {
		unlock()
		t.Fatalf("save: %v", err)
	}
	unlock()

	out := captureStdout(t, func() {
		if err := runList(); err != nil {
			t.Errorf("runList error: %v", err)
		}
	})
	for _, want := range []string{"MODEL", "model-abc", "(not passed)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(default)") && !strings.Contains(out, "TENANT") {
		t.Errorf("model column must not claim (default)")
	}
}

// TestModelDetail: status <handle> の model 行は id + 出所、未指定時は解決先の説明を出す。
func TestModelDetail(t *testing.T) {
	cases := []struct{ model, source, want string }{
		{"m1", "flag", "m1 (source: flag)"},
		{"m1", "config", "m1 (source: config)"},
		{"m1", "", "m1"},
		{"", "", "(not passed; bridge resolves AGENT_HUB_MODEL env or its default)"},
	}
	for _, tc := range cases {
		if got := modelDetail(tc.model, tc.source); got != tc.want {
			t.Errorf("modelDetail(%q,%q) = %q, want %q", tc.model, tc.source, got, tc.want)
		}
	}
}
