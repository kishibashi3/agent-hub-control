package state_test

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// fakeBridge は argv が本物の bridge に見える (argv[0]=bridge-*, "--participant <handle>" を
// 独立 argv として持つ) ダミープロセスを起動して PID を返す。実体は sleep する /bin/sh で、
// -c の後ろの引数は $0 $1... として無視されるので副作用は無い。
func fakeBridge(t *testing.T, handle string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.Args = []string{"bridge-fake", "-c", "sleep 30", "--participant", handle}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake bridge: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// TestIsRunningGhostPID: PID は生きているが (このテストプロセス自身)、この handle の bridge では
// ない → 幽霊 running として false を返す (issue #47)。type 未記録でも検出できることが要点。
func TestIsRunningGhostPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/cmdline")
	}
	for _, typ := range []string{"", "bridge-claude2"} {
		e := &state.Entry{Handle: "ntv-batch-coder", PID: os.Getpid(), BridgeType: typ}
		if e.IsRunning() {
			t.Errorf("BridgeType=%q: live non-bridge PID reported as running (ghost)", typ)
		}
	}
}

// TestIsRunningRealBridgeArgv: argv が本物の bridge の形なら handle 一致で true、別 handle なら false。
func TestIsRunningRealBridgeArgv(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/cmdline")
	}
	pid := fakeBridge(t, "alpha")
	// /proc/<pid>/cmdline が exec 後の argv に置き換わるのを待つ。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !state.LooksLikeBridgeProcess(state.ReadCmdline(pid), "alpha") {
		time.Sleep(10 * time.Millisecond)
	}
	if e := (&state.Entry{Handle: "alpha", PID: pid}); !e.IsRunning() {
		t.Errorf("fake bridge for alpha (pid %d, argv %q) not reported running", pid, state.ReadCmdline(pid))
	}
	if e := (&state.Entry{Handle: "beta", PID: pid}); e.IsRunning() {
		t.Errorf("fake bridge for alpha reported running for handle beta (PID reuse across handles)")
	}
}

// TestIsRunningDeadPID: reap 済み PID は false。
func TestIsRunningDeadPID(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	if e := (&state.Entry{Handle: "x", PID: pid}); e.IsRunning() {
		t.Errorf("reaped pid %d reported running", pid)
	}
}
