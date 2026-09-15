package state_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// fakeBridge は argv と exe が本物の bridge に見える (argv[0]=bridge-*, "--participant <handle>" を
// 独立 argv として持ち、/proc/<pid>/exe の basename も bridge-*) ダミープロセスを起動して PID を返す。
// 実体は /bin/sh を "bridge-fake" という名前でコピーしたもの (issue #50: exe 突合のため symlink 不可)。
// "sleep 30; :" と複合コマンドにして sh の exec 最適化 (exe が sleep に置き換わる) を防ぐ。
// -c の後ろの引数は $0 $1... として無視されるので副作用は無い。
func fakeBridge(t *testing.T, handle string) int {
	t.Helper()
	bin := copyShAs(t, "bridge-fake")
	return startFake(t, exec.Command(bin, "-c", "sleep 30; :", "bridge-fake", "--participant", handle))
}

// spoofedBridge は argv だけ bridge に偽装した /bin/sh (exe は dash 等の実体) を起動して PID を返す。
// `exec -a bridge-x sh` / prctl による argv 偽装に相当し、exe 突合で弾かれるべき (issue #50)。
func spoofedBridge(t *testing.T, handle string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 30; :")
	cmd.Args = []string{"bridge-fake", "-c", "sleep 30; :", "--participant", handle}
	return startFake(t, cmd)
}

func startFake(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake bridge: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

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

// TestIsRunningGhostPID: PID は生きているが (このテストプロセス自身)、この handle の bridge では
// ない → 幽霊 running として false を返す (issue #47)。type 未記録でも検出できることが要点。
func TestIsRunningGhostPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/cmdline and /proc/<pid>/stat")
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
		t.Skip("relies on /proc/<pid>/cmdline and /proc/<pid>/stat")
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

// TestIsRunningSpoofedArgvExe: argv は bridge に見えるが /proc/<pid>/exe が bridge バイナリでない
// (argv 偽装) プロセスは running と判定しない (issue #50)。
func TestIsRunningSpoofedArgvExe(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/cmdline and /proc/<pid>/exe")
	}
	pid := spoofedBridge(t, "alpha")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !bridgeArgvMatches(pid, "alpha") {
		time.Sleep(10 * time.Millisecond)
	}
	if !bridgeArgvMatches(pid, "alpha") {
		t.Fatalf("precondition: spoofed argv %q should pass the argv-only check", mustArgv(pid))
	}
	exe, err := state.ReadExe(pid)
	if err != nil {
		t.Fatalf("precondition: read exe of pid %d: %v", pid, err)
	}
	if state.LooksLikeBridgeExe(exe) {
		t.Fatalf("precondition: /bin/sh resolved to %q which already looks like a bridge", exe)
	}
	if e := (&state.Entry{Handle: "alpha", PID: pid}); e.IsRunning() {
		t.Errorf("argv-spoofed process (exe=%s) reported running", exe)
	}
	if ok, err := state.IsBridgeProcess(pid, "alpha"); err != nil || ok {
		t.Errorf("IsBridgeProcess = (%v, %v), want (false, nil)", ok, err)
	}
}

// procState は /proc/<pid>/stat の state 欄 (R/S/D/Z/T...) を返す。comm は括弧付きで空白や ')' を
// 含みうるので、最後の ')' の後から読む。read / parse 失敗は "" に潰さず error で返し、
// 呼び出し側の診断 (Fatalf) に原因が残るようにする。
func procState(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", fmt.Errorf("/proc/%d/stat: no ')' in %q", pid, s)
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) == 0 {
		return "", fmt.Errorf("/proc/%d/stat: no state field after comm in %q", pid, s)
	}
	return fields[0], nil
}

// mustProcState は procState の失敗を即 Fatalf にする test helper。
func mustProcState(t *testing.T, pid int) string {
	t.Helper()
	st, err := procState(pid)
	if err != nil {
		t.Fatalf("read proc state of pid %d: %v", pid, err)
	}
	return st
}

func mustArgv(pid int) []string {
	argv, _ := state.ReadCmdline(pid)
	return argv
}

// TestIsRunningZombiePID: 終了済みだが未 reap の子 (zombie) は Signal(0) が成功し cmdline が空になる。
// 空 argv を「不明」扱いで true に倒すと幽霊 running が再発するので、false を要求する。
func TestIsRunningZombiePID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/cmdline and /proc/<pid>/stat")
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
	for st := mustProcState(t, pid); st != "Z"; st = mustProcState(t, pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d did not become zombie within 2s (stat state %q)", pid, st)
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

// TestIsRunningExeDeleted: bridge バイナリが稼働中に置き換え・削除されると /proc/<pid>/exe は
// ".../bridge-fake (deleted)" になる (make install で bridge-claude2 を更新した直後の全 bridge が
// この状態)。exe 突合がこれを「bridge ではない」と誤判定すると、更新直後に全 bridge が dead 扱いに
// なり watchdog が重複 spawn する (#50 が防ぎたい failure の逆流)。running 判定は true を維持すること。
func TestIsRunningExeDeleted(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/exe")
	}
	bin := copyShAs(t, "bridge-fake")
	pid := startFake(t, exec.Command(bin, "-c", "sleep 30; :", "bridge-fake", "--participant", "alpha"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !bridgeArgvMatches(pid, "alpha") {
		time.Sleep(10 * time.Millisecond)
	}
	// 稼働中にバイナリを削除 → kernel は exe の readlink に " (deleted)" を付ける。
	if err := os.Remove(bin); err != nil {
		t.Fatalf("remove running binary: %v", err)
	}
	raw, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		t.Fatalf("readlink exe: %v", err)
	}
	if !strings.HasSuffix(raw, " (deleted)") {
		t.Fatalf("precondition: expected ' (deleted)' suffix after removing the binary, got %q", raw)
	}
	exe, err := state.ReadExe(pid)
	if err != nil || strings.HasSuffix(exe, " (deleted)") || !state.LooksLikeBridgeExe(exe) {
		t.Errorf("ReadExe = (%q, %v): want suffix stripped and bridge- prefix kept", exe, err)
	}
	if e := (&state.Entry{Handle: "alpha", PID: pid, BridgeType: "bridge-claude2"}); !e.IsRunning() {
		t.Errorf("bridge with replaced/deleted binary (exe=%q) reported not running", raw)
	}
	if ok, err := state.IsBridgeProcess(pid, "alpha"); err != nil || !ok {
		t.Errorf("IsBridgeProcess = (%v, %v), want (true, nil)", ok, err)
	}
}
