package bridge

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/state"
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
// 実装: /bin/sh の実体を <tmp>/<argv0base> にコピーして起動する。symlink ではなくコピーなのは、
// IsBridgeProcess が /proc/<pid>/exe (kernel が解決した実体パス) の basename も突合するため
// (issue #50): symlink だと exe は /usr/bin/dash 等に解決されて bridge に見えない。
// "-c 'sleep 100; :'" と複合コマンドにすることで sh の exec 最適化 (単一コマンドだと sh 自身が
// その実体に置き換わって argv を失う) を防ぎ、sh プロセスが argv を保持したまま生存する。
// flag/handle は sh の positional 引数 ($0,$1,$2) として渡すと独立 argv 要素として cmdline に残る。
func startFakeProc(t *testing.T, argv0base, flag, handle string) *exec.Cmd {
	t.Helper()
	bin := copyShAs(t, argv0base)
	// exec.Command(bin, ...) は Args[0]=bin にするので argv[0] basename = argv0base になる。
	cmd := exec.Command(bin, "-c", "sleep 100; :", argv0base, flag, handle)
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

	// resolveBinary は「与えられたパス」と「symlink 解決後の実体」両方の basename に "bridge-" prefix を
	// 要求する (issue #50) ので、symlink ではなく fake script を bridge-claude2 という名前で copy する。
	binCopy := filepath.Join(t.TempDir(), "bridge-claude2")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read fake bridge: %v", err)
	}
	if err := os.WriteFile(binCopy, script, 0o755); err != nil {
		t.Fatalf("copy fake bridge: %v", err)
	}
	t.Setenv("AGENT_HUB_BRIDGE_CLAUDE2_BIN", binCopy)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("AGENT_HUB_HOME", t.TempDir()) // 実 state (~/.agent-hub/state/bridges.json) と logs を汚さない (issue #49 / #54)
	t.Setenv(state.BridgeLogDirEnv, "")     // ホスト env の上書きで実ログディレクトリを指さないように
	legacyDir := t.TempDir()
	prevLegacy := legacyLogDir
	legacyLogDir = legacyDir // 互換 symlink も /tmp ではなく tempdir に (issue #54)
	t.Cleanup(func() { legacyLogDir = prevLegacy })

	workdir := t.TempDir()
	const handle = "__test_spawn_args__"
	logPath, err := state.BridgeLogPath(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(logPath, os.Getenv("AGENT_HUB_HOME")) {
		t.Fatalf("log path %q is not under AGENT_HUB_HOME (issue #54)", logPath)
	}

	// 前回テストのゴミプロセスをクリーンアップしてから開始
	pkillHandle(handle)

	t.Cleanup(func() {
		pkillHandle(handle)
	})

	// run spawn in background and wait briefly; it succeeds when "registered and listening" is found
	done := make(chan error, 1)
	go func() {
		done <- runSpawn(handle, "bridge-claude2", workdir, "", "", modelSpec{ID: "test-model-x", Source: modelSourceFlag}, 10)
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

	var foundParticipant, foundUser, foundModel bool
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "--participant") {
			foundParticipant = true
		}
		if strings.Contains(line, "--user") {
			foundUser = true
		}
		if strings.Contains(line, "-model test-model-x") {
			foundModel = true
		}
	}
	if !foundParticipant {
		t.Error("bridge was not called with --participant flag")
	}
	if foundUser {
		t.Error("bridge was called with --user flag (deprecated; should use --participant)")
	}
	if !foundModel {
		t.Error("bridge was not called with -model <id> (issue #46)")
	}

	// state にも model が記録される (restart / watchdog respawn が同じ model で起動し直すため)。
	st, err := state.Load()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if e := st.Get(handle); e == nil || e.Model != "test-model-x" || e.ModelSource != modelSourceFlag {
		t.Errorf("state entry = %+v, want Model=test-model-x ModelSource=flag", e)
	}
	// log_path は新パスを記録し、旧パスには新パスへの symlink が張られる (段階 deprecation、issue #54)。
	if e := st.Get(handle); e != nil && e.LogPath != logPath {
		t.Errorf("state log_path = %q, want %q", e.LogPath, logPath)
	}
	legacy := state.LegacyBridgeLogPath(legacyDir, handle)
	if target, err := os.Readlink(legacy); err != nil || target != logPath {
		t.Errorf("legacy symlink %s -> (%q, %v), want -> %q", legacy, target, err, logPath)
	}
}

// TestLinkLegacyLogPath: 互換 symlink の張り方 (issue #54)。存在しない → 作成、既存 symlink → 張り替え、
// 自分所有の regular file (旧バイナリのログ) → .pre-migration に退避して symlink (PR #71 review Critical 1)。
// 他者所有 regular file は /tmp の sticky bit で Rename が EPERM になる経路で、テストでは再現できない
// (uid を変えられない) ので ownedBySelf の分岐のみ確認する。
func TestLinkLegacyLogPath(t *testing.T) {
	dir := t.TempDir()
	prev := legacyLogDir
	legacyLogDir = dir
	t.Cleanup(func() { legacyLogDir = prev })

	newLog := filepath.Join(t.TempDir(), "bridge-h.out.log")
	legacy := state.LegacyBridgeLogPath(dir, "h")

	linkLegacyLogPath("h", newLog)
	if target, err := os.Readlink(legacy); err != nil || target != newLog {
		t.Fatalf("fresh: readlink = (%q, %v), want %q", target, err, newLog)
	}

	otherLog := filepath.Join(t.TempDir(), "bridge-h.out.log")
	linkLegacyLogPath("h", otherLog)
	if target, err := os.Readlink(legacy); err != nil || target != otherLog {
		t.Fatalf("relink: readlink = (%q, %v), want %q", target, err, otherLog)
	}

	os.Remove(legacy)
	if err := os.WriteFile(legacy, []byte("old binary log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkLegacyLogPath("h", newLog)
	if target, err := os.Readlink(legacy); err != nil || target != newLog {
		t.Fatalf("self-owned regular file: readlink = (%q, %v), want symlink -> %q", target, err, newLog)
	}
	backup := legacy + ".pre-migration"
	if data, err := os.ReadFile(backup); err != nil || string(data) != "old binary log\n" {
		t.Fatalf("old log must be moved to %s intact: (%q, %v)", backup, data, err)
	}

	fi, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if !ownedBySelf(fi) {
		t.Errorf("ownedBySelf(own file) = false")
	}
}

// TestResolveOrphanLogPath: sync が orphan に記録する log_path の解決順 (issue #54)。
// 旧パスが symlink のときは採用しない (他者 symlink を bridges.json に永続化しない、PR #71 review Minor 2)。
func TestResolveOrphanLogPath(t *testing.T) {
	t.Setenv("AGENT_HUB_HOME", t.TempDir())
	t.Setenv(state.BridgeLogDirEnv, "") // ホスト env の上書きで実ログディレクトリに書かないように
	dir := t.TempDir()
	prev := legacyLogDir
	legacyLogDir = dir
	t.Cleanup(func() { legacyLogDir = prev })

	newPath, err := state.BridgeLogPath("h")
	if err != nil {
		t.Fatal(err)
	}
	legacy := state.LegacyBridgeLogPath(dir, "h")

	resolve := func() string {
		t.Helper()
		got, err := resolveOrphanLogPath("h")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := resolve(); got != newPath {
		t.Errorf("neither exists: got %q, want new path %q", got, newPath)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), legacy); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got != newPath {
		t.Errorf("legacy is a symlink: got %q, want new path %q (must not adopt symlink)", got, newPath)
	}
	os.Remove(legacy)
	if err := os.WriteFile(legacy, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got != legacy {
		t.Errorf("only legacy exists: got %q, want %q", got, legacy)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got != newPath {
		t.Errorf("both exist: got %q, want new path %q", got, newPath)
	}

	t.Setenv(state.BridgeLogDirEnv, "relative/logs")
	if _, err := resolveOrphanLogPath("h"); err == nil {
		t.Errorf("relative %s must be an error, not silently recorded", state.BridgeLogDirEnv)
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

// TestResolveBinaryRejectsNonBridgeBasename: bridge バイナリの命名不変条件 ("bridge-" prefix) を
// 満たさないパスは env 経由でも PATH 経由でも spawn 前に拒否する (issue #50)。満たさないまま
// spawn すると IsRunning が恒久的に dead 判定し watchdog が重複 spawn するため。
func TestResolveBinaryRejectsNonBridgeBasename(t *testing.T) {
	dir := t.TempDir()
	writeExec := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	bad := writeExec("claude2-wrapper")
	good := writeExec("bridge-claude2")

	t.Run("env var without bridge- prefix is rejected", func(t *testing.T) {
		t.Setenv("AGENT_HUB_BRIDGE_CLAUDE2_BIN", bad)
		if _, err := resolveBinary("bridge-claude2"); err == nil || !strings.Contains(err.Error(), "bridge-") {
			t.Errorf("expected bridge- invariant error, got %v", err)
		}
	})
	t.Run("env var with bridge- prefix is accepted", func(t *testing.T) {
		t.Setenv("AGENT_HUB_BRIDGE_CLAUDE2_BIN", good)
		got, err := resolveBinary("bridge-claude2")
		if err != nil || got != good {
			t.Errorf("resolveBinary = (%q, %v), want (%q, nil)", got, err, good)
		}
	})
	t.Run("PATH lookup of a non-bridge type is rejected", func(t *testing.T) {
		t.Setenv("AGENT_HUB_CLAUDE2_WRAPPER_BIN", "")
		t.Setenv("PATH", dir)
		if _, err := resolveBinary("claude2-wrapper"); err == nil || !strings.Contains(err.Error(), "bridge-") {
			t.Errorf("expected bridge- invariant error, got %v", err)
		}
	})
	t.Run("PATH lookup of a bridge type is accepted", func(t *testing.T) {
		t.Setenv("AGENT_HUB_BRIDGE_CLAUDE2_BIN", "")
		t.Setenv("PATH", dir)
		got, err := resolveBinary("bridge-claude2")
		if err != nil || got != good {
			t.Errorf("resolveBinary = (%q, %v), want (%q, nil)", got, err, good)
		}
	})

	// /proc/<pid>/exe は symlink 解決後の実体を指すので、bridge-* という名前の symlink が
	// 非 bridge- 実体を指す配置は spawn 時に弾く (review Minor 1)。逆に実体も bridge-* なら受理する。
	linkDir := t.TempDir()
	badLink := filepath.Join(linkDir, "bridge-codex2")
	if err := os.Symlink(bad, badLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	goodLink := filepath.Join(linkDir, "bridge-codex2-alias")
	if err := os.Symlink(good, goodLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Run("env var symlink named bridge-* to a non-bridge target is rejected", func(t *testing.T) {
		t.Setenv("AGENT_HUB_BRIDGE_CODEX2_BIN", badLink)
		_, err := resolveBinary("bridge-codex2")
		if err == nil || !strings.Contains(err.Error(), "symlink target") {
			t.Errorf("expected symlink target rejection, got %v", err)
		}
	})
	t.Run("env var symlink to a bridge-* target is accepted and keeps the given path", func(t *testing.T) {
		t.Setenv("AGENT_HUB_BRIDGE_CODEX2_BIN", goodLink)
		got, err := resolveBinary("bridge-codex2")
		if err != nil || got != goodLink {
			t.Errorf("resolveBinary = (%q, %v), want (%q, nil)", got, err, goodLink)
		}
	})
	t.Run("PATH symlink named bridge-* to a non-bridge target is rejected", func(t *testing.T) {
		t.Setenv("AGENT_HUB_BRIDGE_CODEX2_BIN", "")
		t.Setenv("PATH", linkDir)
		if _, err := resolveBinary("bridge-codex2"); err == nil || !strings.Contains(err.Error(), "symlink target") {
			t.Errorf("expected symlink target rejection, got %v", err)
		}
	})
}
