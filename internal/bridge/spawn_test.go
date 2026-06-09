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

// TestPgrepHandleFindsProcess は --participant <handle> で起動したプロセスを検出できることを確認する。
// sh スクリプトとして起動することで cmdline に --participant <handle> が残り続ける。
func TestPgrepHandleFindsProcess(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	if runtime.GOOS != "linux" {
		t.Skip("pgrep -f behavior is Linux-specific in this test")
	}

	const handle = "__test_bridge_handle_pgrep__"

	_, thisFile, _, _ := runtime.Caller(0)
	scriptPath := filepath.Join(filepath.Dir(thisFile), "testdata", "fake-bridge.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("testdata/fake-bridge.sh not found: %v", err)
	}

	// sh <script> --participant <handle> の形で起動すると sh プロセスの cmdline に
	// "--participant <handle>" が含まれ、pgrep -f で検出できる。
	fake := exec.Command("/bin/sh", scriptPath, "--participant", handle)
	if err := fake.Start(); err != nil {
		t.Fatalf("failed to start fake process: %v", err)
	}
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
