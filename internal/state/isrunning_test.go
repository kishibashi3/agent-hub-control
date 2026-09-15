package state_test

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
	for time.Now().Before(deadline) && !bridgeArgvMatches(pid, "alpha") {
		time.Sleep(10 * time.Millisecond)
	}
	if e := (&state.Entry{Handle: "alpha", PID: pid}); !e.IsRunning() {
		t.Errorf("fake bridge for alpha (pid %d, argv %q) not reported running", pid, mustArgv(pid))
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

func bridgeArgvMatches(pid int, handle string) bool {
	argv, err := state.ReadCmdline(pid)
	return err == nil && state.LooksLikeBridgeProcess(argv, handle)
}

// procState は /proc/<pid>/stat の state 欄 (R/S/D/Z/T...) を返す。comm は括弧付きで空白や ')' を
// 含みうるので、最後の ')' の後から読む。
func procState(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ""
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func isZombie(t *testing.T, pid int) bool {
	t.Helper()
	return procState(t, pid) == "Z"
}

func mustArgv(pid int) []string {
	argv, _ := state.ReadCmdline(pid)
	return argv
}

// TestIsRunningZombiePID: 終了済みだが未 reap の子 (zombie) は Signal(0) が成功し cmdline が空になる。
// 空 argv を「不明」扱いで true に倒すと幽霊 running が再発するので、false を要求する。
func TestIsRunningZombiePID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/cmdline")
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.Args = []string{"bridge-fake", "-c", "exit 0", "--participant", "zed"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Wait() })
	// zombie 化を /proc/<pid>/stat の state 欄 ('Z') で待つ。cmdline が空になるのを待つと、
	// exec 直後の一瞬 (新 mm の arg_start/arg_end 設定前) にも cmdline が空に見えるため
	// 「まだ生きている sh」を zombie と誤認してループを抜け、IsRunning が真の argv を読んで
	// true を返す flake になる (issue #63)。
	deadline := time.Now().Add(2 * time.Second)
	for !isZombie(t, pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d did not become zombie within 2s (stat state %q)", pid, procState(t, pid))
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, typ := range []string{"", "bridge-fake"} {
		e := &state.Entry{Handle: "zed", PID: pid, BridgeType: typ}
		if e.IsRunning() {
			t.Errorf("BridgeType=%q: zombie pid %d (argv %q) reported running", typ, pid, mustArgv(pid))
		}
	}
}
