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

// mustProcState は state.ReadProcState の失敗を即 Fatalf にする test helper。
func mustProcState(t *testing.T, pid int) string {
	t.Helper()
	st, err := state.ReadProcState(pid)
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

// emptyArgvProcess は cmdline が空に見える生きたプロセスを起動して PID を返す。argv を [""] にすると
// /proc/<pid>/cmdline は "\x00" となり ReadCmdline は空 argv を返す — exec 直後 (新 mm の
// arg_start/arg_end 設定前) に status / reconcile が観測する状態と ReadCmdline の見え方が一致する。
// 引数無しの sh は stdin から読むため、pipe を握ったままにして生かしておく。
// 前提: /bin/sh が dash / bash 系であること。busybox sh は argv[0]="" だと applet 名を解決できず
// 即終了するため、その環境では precondition (Z 判定) で Fatalf になる (本ホスト dash / CI ubuntu は可)。
func emptyArgvProcess(t *testing.T, bin string) int {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Args = []string{""}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	pid := startFake(t, cmd)
	// Start() は exec 成功後に戻り、argv は [""] なので cmdline は以後ずっと "\x00" (空 argv) — 待機不要。
	if argv := mustArgv(pid); len(argv) != 0 {
		t.Fatalf("precondition: pid %d argv %q, want empty", pid, argv)
	}
	if st := mustProcState(t, pid); st == "Z" || st == "X" {
		t.Fatalf("precondition: pid %d already dead (stat state %q)", pid, st)
	}
	return pid
}

// TestIsRunningExecWindowEmptyArgv: 生きている (stat が Z 以外) が cmdline が空、かつ exe が bridge
// バイナリを指すプロセスは exec 直後の窓とみなし running を維持する (issue #65)。ここで false に倒すと
// spawn 直後の status / fleet reconcile が生きた bridge を stopped 扱いして二重 spawn する。
func TestIsRunningExecWindowEmptyArgv(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/cmdline, /proc/<pid>/stat and /proc/<pid>/exe")
	}
	pid := emptyArgvProcess(t, copyShAs(t, "bridge-fake"))
	for _, typ := range []string{"", "bridge-fake"} {
		e := &state.Entry{Handle: "alpha", PID: pid, BridgeType: typ}
		if !e.IsRunning() {
			t.Errorf("BridgeType=%q: live bridge exe with empty argv (pid %d, stat %s) reported not running",
				typ, pid, mustProcState(t, pid))
		}
	}
	if ok, err := state.IsBridgeProcess(pid, "alpha"); err != nil || !ok {
		t.Errorf("IsBridgeProcess = (%v, %v), want (true, nil)", ok, err)
	}
	// 既知のトレードオフ: 空 argv では handle を突合できないので、別 handle でも true になる
	// (proc.go emptyArgvIsBridge の doc comment)。挙動を変えたら本 assert を更新すること。
	if ok, err := state.IsBridgeProcess(pid, "other-handle"); err != nil || !ok {
		t.Errorf("IsBridgeProcess(other handle) = (%v, %v), want (true, nil) as documented trade-off", ok, err)
	}
}

// TestIsRunningEmptyArgvUnreadableExe: cmdline が空で生きていても /proc/<pid>/exe が読めない
// (kernel thread の ENOENT、root 所有プロセスを非 root から見た EACCES) PID は false。
// user scope の watchdog が dead bridge の PID を再利用した kworker 等を永久に running 扱いしない
// こと (PR #68 review Critical 1)。ホスト上で該当 PID を探し、無ければ skip。
func TestIsRunningEmptyArgvUnreadableExe(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skip("no /proc")
	}
	for _, d := range entries {
		var pid int
		if _, err := fmt.Sscanf(d.Name(), "%d", &pid); err != nil || pid <= 0 {
			continue
		}
		argv, err := state.ReadCmdline(pid)
		if err != nil || len(argv) != 0 {
			continue
		}
		if st, err := state.ReadProcState(pid); err != nil || st == "Z" || st == "X" {
			continue
		}
		if _, err := state.ReadExe(pid); err == nil {
			continue
		}
		if ok, err := state.IsBridgeProcess(pid, "alpha"); err != nil || ok {
			t.Errorf("pid %d (empty argv, unreadable exe): IsBridgeProcess = (%v, %v), want (false, nil)", pid, ok, err)
		}
		if e := (&state.Entry{Handle: "alpha", PID: pid}); e.IsRunning() {
			t.Errorf("pid %d (empty argv, unreadable exe) reported running", pid)
		}
		return
	}
	t.Skip("no live pid with empty argv and unreadable exe on this host")
}

// TestIsRunningEmptyArgvNonBridgeExe: cmdline が空で生きていても exe が bridge バイナリでなければ
// PID 再利用先の無関係なプロセスなので false。issue #65 の緩和が #47 の幽霊 running を再発させないこと。
func TestIsRunningEmptyArgvNonBridgeExe(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/cmdline, /proc/<pid>/stat and /proc/<pid>/exe")
	}
	pid := emptyArgvProcess(t, "/bin/sh")
	exe, err := state.ReadExe(pid)
	if err != nil {
		t.Fatalf("precondition: read exe of pid %d: %v", pid, err)
	}
	if state.LooksLikeBridgeExe(exe) {
		t.Fatalf("precondition: /bin/sh resolved to %q which already looks like a bridge", exe)
	}
	if e := (&state.Entry{Handle: "alpha", PID: pid}); e.IsRunning() {
		t.Errorf("non-bridge exe %q with empty argv reported running", exe)
	}
	if ok, err := state.IsBridgeProcess(pid, "alpha"); err != nil || ok {
		t.Errorf("IsBridgeProcess = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestReadProcState: 自プロセスは R (running) か S (sleeping)。
func TestReadProcState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc/<pid>/stat")
	}
	st, err := state.ReadProcState(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if st != "R" && st != "S" {
		t.Errorf("own stat state = %q, want R or S", st)
	}
}
