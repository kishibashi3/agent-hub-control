package bridge

import (
	"os/exec"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// fakeBridgePID は IsRunning が「本物の bridge」と判定する argv を持つダミープロセスを起動して
// PID を返す (issue #47 以降、テストプロセス自身の PID は bridge に見えないため alive 代わりに
// 使えない)。/bin/sh -c の後ろの引数は $0 $1... として無視されるので副作用は無い。
func fakeBridgePID(t *testing.T, handle string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.Args = []string{"bridge-fake", "-c", "sleep 30", "--participant", handle}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake bridge: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !state.LooksLikeBridgeProcess(state.ReadCmdline(pid), handle) {
		time.Sleep(10 * time.Millisecond)
	}
	return pid
}
