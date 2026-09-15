package bridge

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// fakeBridgePID は IsRunning が「本物の bridge」と判定する argv / exe を持つダミープロセスを起動して
// PID を返す (issue #47 以降、テストプロセス自身の PID は bridge に見えないため alive 代わりに
// 使えない)。実体は /bin/sh を "bridge-fake" という名前でコピーしたもの: IsRunning は
// /proc/<pid>/exe の basename も突合するため (issue #50)、/bin/sh を直接起動して argv[0] だけ
// 偽装すると exe=dash で bridge に見えない。"sleep 30; :" と複合コマンドにして sh の exec 最適化を防ぐ。
// -c の後ろの引数は $0 $1... として無視されるので副作用は無い。
func fakeBridgePID(t *testing.T, handle string) int {
	t.Helper()
	bin := copyShAs(t, "bridge-fake")
	cmd := exec.Command(bin, "-c", "sleep 30; :", "bridge-fake", "--participant", handle)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake bridge: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !bridgeArgvMatches(pid, handle) {
		time.Sleep(10 * time.Millisecond)
	}
	return pid
}

func bridgeArgvMatches(pid int, handle string) bool {
	ok, err := state.IsBridgeProcess(pid, handle)
	return err == nil && ok
}

// copyShAs は /bin/sh の実体を <tmp>/<name> にコピーして実行可能パスを返す。
// /proc/<pid>/exe がこのコピーを指すので、name を "bridge-*" にすれば exe 突合を通る fake になる。
func copyShAs(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("/bin/sh")
	if err != nil {
		t.Fatalf("read /bin/sh: %v", err)
	}
	bin := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return bin
}
