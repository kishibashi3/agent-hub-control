package bridge

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// reapedPID は起動してすぐ終了・回収したプロセスの PID を返す。
// この PID は確実に「存在しない（死んでいる）」ので dead 判定のテストに使える。
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "0")
	if err := cmd.Run(); err != nil { // Run は終了まで待って reap する
		t.Fatalf("failed to run/reap helper process: %v", err)
	}
	return cmd.Process.Pid
}

// TestDeadEntries は生存プロセスを alive、回収済み PID を dead と判定し、
// dead のみが Handle 昇順で返ることを確認する。
func TestDeadEntries(t *testing.T) {
	st := &state.State{Bridges: map[string]*state.Entry{
		"zeta":  {Handle: "zeta", PID: reapedPID(t)},  // dead
		"alive": {Handle: "alive", PID: os.Getpid()},  // alive（このテストプロセス自身）
		"alpha": {Handle: "alpha", PID: reapedPID(t)}, // dead
	}}

	dead := deadEntries(st)
	if len(dead) != 2 {
		t.Fatalf("expected 2 dead entries, got %d: %+v", len(dead), dead)
	}
	// Handle 昇順（alpha < zeta）でソートされていること
	if dead[0].Handle != "alpha" || dead[1].Handle != "zeta" {
		t.Errorf("expected sorted [alpha zeta], got [%s %s]", dead[0].Handle, dead[1].Handle)
	}
}

// captureStdout は f 実行中の os.Stdout 出力を文字列で返す。
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			sb.WriteString(sc.Text())
			sb.WriteString("\n")
		}
		done <- sb.String()
	}()

	f()

	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// TestRunWatchdogAllAlive は全 bridge 生存時に "all N bridges alive" を出力することを確認する。
func TestRunWatchdogAllAlive(t *testing.T) {
	t.Setenv("AGENT_HUB_HOME", t.TempDir())

	st, unlock, err := state.LoadLocked()
	if err != nil {
		t.Fatalf("LoadLocked: %v", err)
	}
	// BridgeType を空にすると IsRunning は /proc/comm 突合をスキップし、自プロセス PID で alive 判定になる。
	st.Set("self", os.Getpid(), "", "/tmp", "", "/tmp/self.log")
	if err := st.Save(); err != nil {
		unlock()
		t.Fatalf("Save: %v", err)
	}
	unlock()

	out := captureStdout(t, func() {
		if err := RunWatchdog(); err != nil {
			t.Errorf("RunWatchdog: %v", err)
		}
	})

	if !strings.Contains(out, "all 1 bridges alive") {
		t.Errorf("expected 'all 1 bridges alive', got: %q", out)
	}
}

// TestRunWatchdogCleansDeadOnRespawnFailure は、死んだ bridge を state から削除して
// 書き戻すこと、respawn に失敗しても（存在しない bridge type）クリーンアップが行われることを確認する。
func TestRunWatchdogCleansDeadOnRespawnFailure(t *testing.T) {
	t.Setenv("AGENT_HUB_HOME", t.TempDir())

	st, unlock, err := state.LoadLocked()
	if err != nil {
		t.Fatalf("LoadLocked: %v", err)
	}
	st.Set("alive", os.Getpid(), "", "/tmp", "", "/tmp/alive.log")
	// 死んだエントリ。BridgeType は PATH に存在しないので runSpawn は resolveBinary で fail-fast し、
	// 実プロセスは起動しない（テスト副作用なし）。
	st.Bridges["dead"] = &state.Entry{
		Handle:     "dead",
		PID:        reapedPID(t),
		BridgeType: "__nonexistent_bridge_type__",
		Workdir:    "/tmp",
	}
	if err := st.Save(); err != nil {
		unlock()
		t.Fatalf("Save: %v", err)
	}
	unlock()

	// stderr に "respawn failed" が出る。stdout のみキャプチャすれば十分。
	out := captureStdout(t, func() {
		if err := RunWatchdog(); err != nil {
			t.Errorf("RunWatchdog: %v", err)
		}
	})

	if !strings.Contains(out, "@dead dead") || !strings.Contains(out, "respawning...") {
		t.Errorf("expected dead detection line, got: %q", out)
	}

	// state を読み直して dead が削除され、alive が残っていることを確認する。
	st2, err := state.Load()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if st2.Get("dead") != nil {
		t.Errorf("expected dead entry removed from state, still present: %+v", st2.Get("dead"))
	}
	if st2.Get("alive") == nil {
		t.Errorf("expected alive entry to be preserved, but it was removed")
	}
}
