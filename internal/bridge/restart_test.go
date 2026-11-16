package bridge

import (
	"runtime"
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
	restartSpawn = func(participant, bridgeType, workdir, tenantFlag, displayName string, model modelSpec, timeoutS int) error {
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
