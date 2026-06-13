package bridge

import (
	"bufio"
	"fmt"
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

// TestSpawnBridgeArgs は runSpawn が bridge-claude2 を起動するとき --participant フラグ（--user ではなく）を
// 渡していることを確認するリグレッションテスト。
// fake-bridge-args.sh を使うことで実際のバイナリなしに検証する。
func TestSpawnBridgeArgs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test is Linux-specific")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	scriptPath := filepath.Join(filepath.Dir(thisFile), "testdata", "fake-bridge-args.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("testdata/fake-bridge-args.sh not found: %v", err)
	}

	t.Setenv("AGENT_HUB_BRIDGE_CLAUDE2_BIN", scriptPath)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	workdir := t.TempDir()
	const handle = "__test_spawn_args__"
	logPath := fmt.Sprintf("/tmp/bridge-%s.log", handle)

	// 前回テストのゴミプロセスをクリーンアップしてから開始
	pkillHandle(handle)
	os.Remove(logPath)

	t.Cleanup(func() {
		pkillHandle(handle)
		os.Remove(logPath)
	})

	// run spawn in background and wait briefly; it succeeds when "registered and listening" is found
	done := make(chan error, 1)
	go func() {
		done <- runSpawn(handle, "bridge-claude2", workdir, "", "", 10)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runSpawn failed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runSpawn timed out")
	}

	// ログファイルに --participant が含まれ、--user が含まれないことを確認
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()

	var foundParticipant, foundUser bool
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "--participant") {
			foundParticipant = true
		}
		if strings.Contains(line, "--user") {
			foundUser = true
		}
	}
	if !foundParticipant {
		t.Error("bridge was not called with --participant flag")
	}
	if foundUser {
		t.Error("bridge was called with --user flag (deprecated; should use --participant)")
	}
}

// pkillHandle はテスト後のクリーンアップ用。--participant または --user で起動した fake プロセスを終了する。
func pkillHandle(handle string) {
	for _, flag := range []string{"--participant", "--user"} {
		exec.Command("pkill", "-f", flag+" "+handle).Run() //nolint:errcheck
	}
}

// TestPgrepHandleFindsProcess は本物の bridge プロセス (argv[0] basename が "bridge-" で始まり、
// --participant <handle> または旧 --user <handle> を独立 argv に持つ) を pgrepHandle が
// 検出できることを確認する。--user は v0.3.0 以前の旧フラグ名で、旧バイナリで起動した orphan も
// 後方互換として検出できなければならない。両フラグを startFakeProc で起動して検証する。
func TestPgrepHandleFindsProcess(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	if runtime.GOOS != "linux" {
		t.Skip("pgrep -f / procfs behavior is Linux-specific in this test")
	}

	for _, flag := range []string{"--participant", "--user"} {
		flag := flag
		t.Run(flag, func(t *testing.T) {
			handle := "__test_bridge_handle_pgrep_" + strings.TrimPrefix(flag, "--") + "__"

			fake := startFakeProc(t, "bridge-fake", flag, handle)
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
				t.Errorf("pgrepHandle should find pid=%d for %s, got 0", fakePID, flag)
				return
			}
			// 返ってきた PID が fake のもの、または pgrep 結果に fake PID が含まれることを確認
			if pid != fakePID {
				out, _ := exec.Command("pgrep", "-f", "--", flag+" "+handle).Output()
				pids := strings.Fields(strings.TrimSpace(string(out)))
				found := false
				for _, p := range pids {
					if v, _ := strconv.Atoi(p); v == fakePID {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("fake pid=%d not found in pgrep output for %s: %v", fakePID, flag, pids)
				}
			}
		})
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
