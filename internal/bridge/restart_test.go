package bridge

import (
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// stubRestartSpawn は re-spawn を記録だけする stub に差し替え、呼ばれた時点で pid がまだ生きていたかを返す。
func stubRestartSpawn(t *testing.T, pid int) <-chan bool {
	t.Helper()
	called := make(chan bool, 1)
	orig := restartSpawn
	restartSpawn = func(participant, bridgeType, workdir, tenantFlag, displayName string, model modelSpec, bin binSpec, timeoutS int) error {
		called <- syscall.Kill(pid, 0) == nil
		return nil
	}
	t.Cleanup(func() { restartSpawn = orig })
	return called
}

// setupRestartEntry は隔離した AGENT_HUB_HOME / XDG_CONFIG_HOME の state に handle → pid のエントリを書く。
func setupRestartEntry(t *testing.T, handle string, pid int) {
	t.Helper()
	t.Setenv("AGENT_HUB_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, unlock, err := state.LoadLocked()
	if err != nil {
		t.Fatalf("load locked: %v", err)
	}
	defer unlock()
	st.Set(handle, pid, "bridge-claude2", t.TempDir(), "", "", "", "/tmp/log")
	if err := st.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// TestRunRestartWaitsForDrainBeforeRespawn は、restart が SIGTERM 後に drain が終わるまで待ってから
// re-spawn することを確認する (issue #92: 以前は 15 秒固定で、超えるとエラー終了して re-spawn しなかった)。
func TestRunRestartWaitsForDrainBeforeRespawn(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	handle := "restart_drain_test_handle_c41e"
	cmd, done := startTermDelayProc(t, handle, "1.5")
	setupRestartEntry(t, handle, cmd.Process.Pid)
	called := stubRestartSpawn(t, cmd.Process.Pid)

	start := time.Now()
	if err := runRestart(handle, "", 1, 10*time.Second); err != nil {
		t.Fatalf("runRestart: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 1400*time.Millisecond {
		t.Errorf("runRestart returned after %s, before the drain (1.5s) finished", elapsed)
	}
	select {
	case alive := <-called:
		if alive {
			t.Error("re-spawn was called while the old process was still alive")
		}
	default:
		t.Fatal("re-spawn was not called")
	}
	if err := <-done; err != nil {
		t.Errorf("process should exit cleanly after drain, got %v (SIGKILL=%v)", err, killedBySIGKILL(err))
	}
}

// TestRunRestartKillsAfterStopTimeoutAndRespawns は、--stop-timeout を超えても終わらないプロセスは
// SIGKILL してから re-spawn すること (エラー終了で止まったままにしない) を確認する。
func TestRunRestartKillsAfterStopTimeoutAndRespawns(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	handle := "restart_kill_test_handle_9d02"
	cmd, done := startTermDelayProc(t, handle, "30")
	setupRestartEntry(t, handle, cmd.Process.Pid)
	called := stubRestartSpawn(t, cmd.Process.Pid)

	start := time.Now()
	if err := runRestart(handle, "", 1, 500*time.Millisecond); err != nil {
		t.Fatalf("runRestart: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("runRestart should give up waiting after ~500ms, took %s", elapsed)
	}
	select {
	case alive := <-called:
		if alive {
			t.Error("re-spawn was called while the old process was still alive")
		}
	default:
		t.Fatal("re-spawn was not called")
	}
	if err := <-done; !killedBySIGKILL(err) {
		t.Errorf("expected process to be SIGKILLed, got %v", err)
	}
}

// TestRunRestartKeepsStateEntryWhileDraining は、drain を待っている間も state のエントリが残っていることを
// 確認する (issue #97: 待機中に restart が中断されても、エントリがあれば後から re-spawn できる)。
func TestRunRestartKeepsStateEntryWhileDraining(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	handle := "restart_keep_test_handle_5b17"
	cmd, done := startTermDelayProc(t, handle, "1.5")
	pid := cmd.Process.Pid
	setupRestartEntry(t, handle, pid)
	called := stubRestartSpawn(t, pid)

	errc := make(chan error, 1)
	go func() { errc <- runRestart(handle, "", 1, 10*time.Second) }()

	time.Sleep(700 * time.Millisecond)
	st, err := state.Load()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if e := st.Get(handle); e == nil || e.PID != pid {
		t.Errorf("state entry should remain (pid=%d) while draining, got %+v", pid, e)
	}

	if err := <-errc; err != nil {
		t.Fatalf("runRestart: %v", err)
	}
	select {
	case <-called:
	default:
		t.Fatal("re-spawn was not called")
	}
	st, err = state.Load()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if e := st.Get(handle); e != nil {
		t.Errorf("old entry should be removed before re-spawn (stub spawn does not re-add), got %+v", e)
	}
	<-done
}

// TestRunRestartDoesNotRespawnWhenAnotherSpawnWon は、drain 待ちの間に別の spawn (fleet の bridge start --all 等) が同じ handle の
// エントリを別 PID で書き換えた場合、restart は二重に re-spawn せずエラーで終わり、そのエントリを
// 消さないことを確認する (issue #97 案 A で許容した競合)。
func TestRunRestartDoesNotRespawnWhenAnotherSpawnWon(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	handle := "restart_race_test_handle_e3a8"
	cmd, _ := startTermDelayProc(t, handle, "1.5")
	pid := cmd.Process.Pid
	setupRestartEntry(t, handle, pid)
	called := stubRestartSpawn(t, pid)

	errc := make(chan error, 1)
	go func() { errc <- runRestart(handle, "", 1, 10*time.Second) }()

	// fleet が先に re-spawn した状態を模す: エントリを別 PID (このテストプロセス) に書き換える。
	time.Sleep(500 * time.Millisecond)
	newPID := os.Getpid()
	func() {
		st, unlock, err := state.LoadLocked()
		if err != nil {
			t.Fatalf("load locked: %v", err)
		}
		defer unlock()
		st.Set(handle, newPID, "bridge-claude2", t.TempDir(), "", "", "", "/tmp/log")
		if err := st.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}
	}()

	err := <-errc
	if err == nil || !strings.Contains(err.Error(), "not re-spawning") {
		t.Fatalf("runRestart should fail without re-spawning, got %v", err)
	}
	select {
	case <-called:
		t.Error("re-spawn must not be called when the entry was replaced")
	default:
	}
	st, lerr := state.Load()
	if lerr != nil {
		t.Fatalf("load state: %v", lerr)
	}
	if e := st.Get(handle); e == nil || e.PID != newPID {
		t.Errorf("entry written by the other spawn must be kept (pid=%d), got %+v", newPID, e)
	}
}

// TestRunRestartDoesNotRespawnWhenEntryRemoved は、drain 待ちの間に stop / prune / sync が同じ handle の
// エントリを消した場合、restart は re-spawn せず、「別の spawn が起動した」ではなく「エントリが消えた」と
// 分かるエラーで終わることを確認する (PR #100 review Minor 1/2)。
func TestRunRestartDoesNotRespawnWhenEntryRemoved(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	handle := "restart_removed_test_handle_9c41"
	cmd, _ := startTermDelayProc(t, handle, "1.5")
	pid := cmd.Process.Pid
	setupRestartEntry(t, handle, pid)
	called := stubRestartSpawn(t, pid)

	errc := make(chan error, 1)
	go func() { errc <- runRestart(handle, "", 1, 10*time.Second) }()

	// bridge stop / prune / sync がエントリを消した状態を模す。
	time.Sleep(500 * time.Millisecond)
	func() {
		st, unlock, err := state.LoadLocked()
		if err != nil {
			t.Fatalf("load locked: %v", err)
		}
		defer unlock()
		st.Delete(handle)
		if err := st.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}
	}()

	err := <-errc
	if err == nil || !strings.Contains(err.Error(), "was removed") || strings.Contains(err.Error(), "re-spawned by fleet") {
		t.Fatalf("runRestart should fail with an entry-removed error, got %v", err)
	}
	select {
	case <-called:
		t.Error("re-spawn must not be called when the entry was removed")
	default:
	}
	st, lerr := state.Load()
	if lerr != nil {
		t.Fatalf("load state: %v", lerr)
	}
	if e := st.Get(handle); e != nil {
		t.Errorf("entry must stay removed, got %+v", e)
	}
}
