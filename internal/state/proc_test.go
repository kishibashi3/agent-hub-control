package state_test

import (
	"testing"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// TestLooksLikeBridgeExe は /proc/<pid>/exe の basename 判定を実プロセスなしで検証する。
// kernel が付ける " (deleted)" suffix は ReadExe 側で除去される前提だが、prefix 判定なので
// 付いたままでも通ることを確認する (実行中にバイナリを置き換えた bridge を dead 扱いしない)。
func TestLooksLikeBridgeExe(t *testing.T) {
	cases := []struct {
		exe  string
		want bool
	}{
		{"/home/u/app/agent-hub-bridges/bridge-claude2/bridge-claude2", true},
		{"/usr/local/bin/bridge-gemini", true},
		{"bridge-fake", true},
		{"/home/u/app/bridge-claude2/bridge-claude2 (deleted)", true},
		{"/usr/bin/dash", false},
		{"/usr/bin/sleep", false},
		{"/usr/local/bin/agenthubctl", false},
		{"/opt/wrappers/claude2-bridge", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := state.LooksLikeBridgeExe(tc.exe); got != tc.want {
			t.Errorf("LooksLikeBridgeExe(%q) = %v, want %v", tc.exe, got, tc.want)
		}
	}
}

// TestIsBridgeProcessUnreadableCmdline: 存在しない PID は cmdline が読めないので error を返し、
// 呼び出し側が従来フォールバックを選べる (false, nil と区別される)。
func TestIsBridgeProcessUnreadableCmdline(t *testing.T) {
	if _, err := state.IsBridgeProcess(1<<22+12345, "x"); err == nil {
		t.Error("expected error for a non-existent PID, got nil")
	}
}
