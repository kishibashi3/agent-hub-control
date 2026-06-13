package bridge

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPgrepHandleNoMatch は存在しないハンドルに対して 0 が返ることを確認する。
func TestPgrepHandleNoMatch(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}

	pid, err := pgrepHandle("__nonexistent_handle_xyz_9999__")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 0 {
		t.Errorf("expected pid=0 for nonexistent handle, got pid=%d", pid)
	}
}

// startFakeProc は long-lived なプロセスを起動し、その cmdline (argv) を制御する。
// argv0base は argv[0] の basename になり (これで bridge 判定される)、flag/handle は
// 「独立した argv 要素」として渡され pgrep -f / looksLikeBridgeProcess の両方の対象になる。
//
// 実装: <tmp>/<argv0base> を /bin/sh への symlink にして起動する。symlink を直接 exec すると
// argv[0] は呼び出しパス (= bridge-fake 等) のまま保たれる (shebang スクリプトと違い置換されない)。
// "-c 'sleep 100; :'" と複合コマンドにすることで sh の exec 最適化 (単一コマンドだと sh 自身が
// その実体に置き換わって argv を失う) を防ぎ、sh プロセスが argv を保持したまま生存する。
// flag/handle は sh の positional 引数 ($0,$1,$2) として渡すと独立 argv 要素として cmdline に残る。
func startFakeProc(t *testing.T, argv0base, flag, handle string) *exec.Cmd {
	t.Helper()
	link := filepath.Join(t.TempDir(), argv0base)
	if err := os.Symlink("/bin/sh", link); err != nil {
		t.Fatalf("symlink fake binary: %v", err)
	}
	// exec.Command(link, ...) は Args[0]=link にするので argv[0] basename = argv0base になる。
	cmd := exec.Command(link, "-c", "sleep 100; :", argv0base, flag, handle)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start fake process: %v", err)
	}
	return cmd
}

// TestPgrepHandleFindsProcess は本物の bridge プロセス (argv[0] basename が "bridge-" で始まり、
// --participant <handle> を独立 argv に持つ) を pgrepHandle が検出できることを確認する。
func TestPgrepHandleFindsProcess(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	if runtime.GOOS != "linux" {
		t.Skip("pgrep -f / procfs behavior is Linux-specific in this test")
	}

	const handle = "__test_bridge_handle_pgrep__"

	fake := startFakeProc(t, "bridge-fake", "--participant", handle)
	defer func() {
		_ = fake.Process.Kill()
		_ = fake.Wait()
	}()

	fakePID := fake.Process.Pid

	// プロセスが cmdline を確定するまで少し待つ
	time.Sleep(100 * time.Millisecond)

	pid, err := pgrepHandle(handle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid == 0 {
		t.Errorf("expected pgrepHandle to find pid=%d, got 0", fakePID)
		return
	}
	// 返ってきた PID が fake のもの、または pgrep 結果に fake PID が含まれることを確認
	if pid != fakePID {
		out, _ := exec.Command("pgrep", "-f", "--", "--participant "+handle).Output()
		pids := strings.Fields(strings.TrimSpace(string(out)))
		found := false
		for _, p := range pids {
			if v, _ := strconv.Atoi(p); v == fakePID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("fake pid=%d not found in pgrep output: %v", fakePID, pids)
		}
	}
}

// TestPgrepHandleIgnoresShellWrapper は issue #31 のリグレッションテスト。
// "bash -c '... --participant <handle> ...'" ラッパーシェルは cmdline に
// "--participant <handle>" を含むため pgrep -f には引っかかるが、実プロセス
// (本物の bridge) ではないので pgrepHandle は検出してはならない。
// このラッパーは Claude Code の Bash ツールや CI/自動化からの spawn 呼び出しを模す。
func TestPgrepHandleIgnoresShellWrapper(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if runtime.GOOS != "linux" {
		t.Skip("pgrep -f / procfs behavior is Linux-specific in this test")
	}

	const handle = "__test_wrapper_false_positive__"

	// bash -c '... --participant <handle> ...' の親ラッパーを再現する。
	// argv[0]=bash で --participant は -c 文字列の中にあるだけ (独立 argv ではない)。
	// 末尾の "; :" で複合コマンドにし、bash の exec 最適化 (単一コマンドだと bash 自身が
	// 実体プロセスに置き換わって cmdline から文字列が消える) を防いで bash を生存させる。
	script := "sleep 100; : agenthubctl bridge spawn --participant " + handle + " --workdir /tmp"
	wrapper := exec.Command("/bin/bash", "-c", script)
	if err := wrapper.Start(); err != nil {
		t.Fatalf("failed to start wrapper: %v", err)
	}
	defer func() {
		_ = wrapper.Process.Kill()
		_ = wrapper.Wait()
	}()

	time.Sleep(100 * time.Millisecond)

	// 前提確認: pgrep -f はこのラッパーに実際にマッチする (= 誤検知の素地がある)
	out, _ := exec.Command("pgrep", "-f", "--", "--participant "+handle).Output()
	if len(strings.Fields(strings.TrimSpace(string(out)))) == 0 {
		t.Skip("pgrep did not match the wrapper; cannot exercise the false-positive path")
	}

	pid, err := pgrepHandle(handle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 0 {
		t.Errorf("pgrepHandle must ignore the bash -c wrapper, but reported pid=%d as running", pid)
	}
}

// TestLooksLikeBridgeProcess は cmdline 分類ロジックを実プロセスなしで網羅的に検証する。
func TestLooksLikeBridgeProcess(t *testing.T) {
	const handle = "alice"
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"real bridge --participant", []string{"/usr/local/bin/bridge-claude2", "--participant", "alice", "--workdir", "/x"}, true},
		{"real bridge -p short", []string{"/usr/local/bin/bridge-gemini", "-p", "alice"}, true},
		{"real bridge legacy --user", []string{"bridge-claude2", "--user", "alice"}, true},
		{"real bridge --participant=eq", []string{"bridge-codex2", "--participant=alice"}, true},
		{"bash -c wrapper", []string{"bash", "-c", "agenthubctl bridge spawn --participant alice --workdir /x"}, false},
		{"sh wrapper", []string{"/bin/sh", "/tmp/run.sh", "--participant", "alice"}, false},
		{"agenthubctl itself", []string{"agenthubctl", "bridge", "spawn", "--participant", "alice"}, false},
		{"bridge but different handle", []string{"bridge-claude2", "--participant", "bob"}, false},
		{"bridge without handle flag", []string{"bridge-claude2", "--workdir", "/x"}, false},
		{"empty argv", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeBridgeProcess(tc.argv, handle); got != tc.want {
				t.Errorf("looksLikeBridgeProcess(%q, %q) = %v, want %v", tc.argv, handle, got, tc.want)
			}
		})
	}
}
