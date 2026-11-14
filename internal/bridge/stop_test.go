package bridge

import (
	"errors"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// TestDefaultStopTimeout は stop の既定待ち時間が bridge の drain 上限
// (compact 5m + subprocess timeout + 1m) + 余裕 1m になっていることを確認する (issue #89)。
func TestDefaultStopTimeout(t *testing.T) {
	cases := []struct {
		subprocessTimeoutS int
		want               time.Duration
	}{
		{0, 37 * time.Minute},    // config 未設定 = bridge 既定 30m
		{600, 17 * time.Minute},  // config で 10m を渡している場合
		{3600, 67 * time.Minute}, // config で 1h を渡している場合
	}
	for _, c := range cases {
		if got := defaultStopTimeout(c.subprocessTimeoutS); got != c.want {
			t.Errorf("defaultStopTimeout(%d) = %s, want %s", c.subprocessTimeoutS, got, c.want)
		}
	}
}

// startTermDelayProc は SIGTERM を受けてから delay 秒後に exit 0 する (drain 相当) fake bridge を起動する。
// argv[0] / exe を bridge-claude2 にするので state.Entry.IsRunning の実プロセス突合を通る。
// 終了したプロセスは goroutine で reap して zombie にしない (zombie は Signal(0) が成功し続けるため)。
func startTermDelayProc(t *testing.T, handle, delay string) (*exec.Cmd, <-chan error) {
	t.Helper()
	bin := copyShAs(t, "bridge-claude2")
	script := "trap 'sleep " + delay + "; exit 0' TERM; while :; do sleep 0.05; done"
	cmd := exec.Command(bin, "-c", script, "bridge-claude2", "--participant", handle)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake bridge: %v", err)
	}
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		err := cmd.Wait()
		close(exited)
		done <- err
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	// trap が設定される前に SIGTERM が届くと即死するので、少し待つ
	time.Sleep(200 * time.Millisecond)
	return cmd, done
}

func killedBySIGKILL(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

// TestStopPIDWaitsForDrain は、SIGTERM 後に drain で数秒かかるプロセスを timeout 以内なら
// SIGKILL せずに待ち切ること (issue #89: 以前は 5 秒固定で SIGKILL していた) を確認する。
func TestStopPIDWaitsForDrain(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	cmd, done := startTermDelayProc(t, "stop_drain_test_handle_3b7d", "1")

	start := time.Now()
	if err := stopPID("stop_drain_test_handle_3b7d", cmd.Process.Pid, 10*time.Second); err != nil {
		t.Fatalf("stopPID: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("stopPID returned after %s, before the drain (1s) finished", elapsed)
	}
	if err := <-done; err != nil {
		t.Errorf("process should exit cleanly after drain, got %v (SIGKILL=%v)", err, killedBySIGKILL(err))
	}
}

// TestStopPIDKillsAfterTimeout は、timeout を過ぎても終わらないプロセスには SIGKILL を送ること を確認する。
func TestStopPIDKillsAfterTimeout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	cmd, done := startTermDelayProc(t, "stop_kill_test_handle_5e21", "30")

	start := time.Now()
	if err := stopPID("stop_kill_test_handle_5e21", cmd.Process.Pid, 500*time.Millisecond); err != nil {
		t.Fatalf("stopPID: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("stopPID should give up after ~500ms, took %s", elapsed)
	}
	select {
	case err := <-done:
		if !killedBySIGKILL(err) {
			t.Errorf("expected process to be SIGKILLed, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process still alive 5s after SIGKILL")
	}
}

// TestRunStopReleasesLockWhileDraining は、drain を待っている間は state lock を手放し、
// プロセスが消えた後でエントリを消すことを確認する (待ちが最大数十分になるため、issue #89)。
func TestRunStopReleasesLockWhileDraining(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}
	t.Setenv("AGENT_HUB_HOME", t.TempDir())
	handle := "stop_lock_test_handle_8a4c"
	cmd, _ := startTermDelayProc(t, handle, "1.5")

	st, unlock, err := state.LoadLocked()
	if err != nil {
		t.Fatalf("load locked: %v", err)
	}
	st.Set(handle, cmd.Process.Pid, "bridge-claude2", "/tmp/wd", "", "", "", "/tmp/log")
	if err := st.Save(); err != nil {
		unlock()
		t.Fatalf("save: %v", err)
	}
	unlock()

	stopDone := make(chan error, 1)
	go func() { stopDone <- runStop(handle, 10*time.Second) }()

	// drain 中 (SIGTERM 後 1.5s) に別の lock 取得が通ること、エントリがまだ残っていることを確認する。
	time.Sleep(500 * time.Millisecond)
	locked := make(chan *state.State, 1)
	go func() {
		s, u, err := state.LoadLocked()
		if err != nil {
			locked <- nil
			return
		}
		u()
		locked <- s
	}()
	select {
	case s := <-locked:
		if s == nil {
			t.Fatal("LoadLocked failed during drain")
		}
		if s.Get(handle) == nil {
			t.Error("entry should stay in state while the bridge is still draining")
		}
	case <-time.After(time.Second):
		t.Fatal("state lock is held while waiting for the drain")
	}

	if err := <-stopDone; err != nil {
		t.Fatalf("runStop: %v", err)
	}
	reloaded, err := state.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Get(handle) != nil {
		t.Error("entry should be removed after the bridge exited")
	}
}
