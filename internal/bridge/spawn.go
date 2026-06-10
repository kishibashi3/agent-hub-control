// spawn.go — bridge spawn command
package bridge

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/config"
	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

const (
	defaultSpawnTimeoutS = 30
	defaultBridgeType    = "bridge-claude2"
)

func NewSpawnCmd() *cobra.Command {
	var (
		workdir    string
		tenant     string
		bridgeType string
		timeout    int
	)

	cmd := &cobra.Command{
		Use:   "spawn --participant <handle> --workdir <path>",
		Short: "Spawn a bridge worker",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			participant, _ := cmd.Flags().GetString("participant")
			if participant == "" {
				if u, _ := cmd.Flags().GetString("user"); u != "" {
					participant = u
				}
			}
			if participant == "" {
				return fmt.Errorf("--participant is required")
			}
			return runSpawn(participant, bridgeType, workdir, tenant, timeout)
		},
	}

	cmd.Flags().StringP("participant", "p", "", "agent-hub handle (without @) [required]")
	cmd.Flags().StringP("user", "u", "", "alias for --participant (deprecated)")
	_ = cmd.Flags().MarkDeprecated("user", "use --participant / -p instead")
	cmd.Flags().StringVarP(&workdir, "workdir", "w", "", "peer workdir with CLAUDE.md (default: cwd)")
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant ID (overrides AGENT_HUB_TENANT env)")
	cmd.Flags().StringVar(&bridgeType, "type", defaultBridgeType, "bridge type (bridge-claude2, bridge-codex2, bridge-gemini, …)")
	cmd.Flags().IntVar(&timeout, "timeout", defaultSpawnTimeoutS, "seconds to wait for ready signal")

	return cmd
}

func runSpawn(participant, bridgeType, workdir, tenantFlag string, timeoutS int) error {
	binary, err := resolveBinary(bridgeType)
	if err != nil {
		return err
	}

	wd := workdir
	if wd == "" {
		wd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}
	wd, err = filepath.Abs(wd)
	if err != nil {
		return fmt.Errorf("abs workdir: %w", err)
	}
	if _, err := os.Stat(wd); err != nil {
		return fmt.Errorf("workdir %q: %w", wd, err)
	}

	tenant := tenantFlag
	if tenant == "" {
		tenant = os.Getenv("AGENT_HUB_TENANT")
	}

	logPath := fmt.Sprintf("/tmp/bridge-%s.log", participant)

	// 既存プロセスチェック（ロック取得 → チェック → プロセス起動 → 保存 → 解放）
	// ロックを保持したまま start + save まで完了させることで並列 spawn による JSON 破損を防ぐ。
	// ready 待機はロック解放後に行い、長時間ロック保持を回避する。
	st, unlock, err := state.LoadLocked()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	if entry := st.Get(participant); entry != nil && entry.IsRunning() {
		unlock()
		return fmt.Errorf("@%s is already running (pid=%d). Use `bridge stop %s` first.", participant, entry.PID, participant)
	}

	// bridges.json に記録がなくても orphan プロセスが残っている場合を検出する (issue #14)
	if pid, err := pgrepHandle(participant); err != nil {
		unlock()
		return fmt.Errorf("pgrep check: %w", err)
	} else if pid != 0 {
		unlock()
		return fmt.Errorf("@%s is already running (pid=%d). Use `bridge stop %s` first.", participant, pid, participant)
	}

	// ログファイルをクリア
	logFile, err := os.Create(logPath)
	if err != nil {
		unlock()
		return fmt.Errorf("create log file: %w", err)
	}
	logFile.Close()

	// bridge 起動
	args := []string{"--participant", participant, "--workdir", wd}
	if tenant != "" {
		args = append(args, "--tenant", tenant)
	}
	if bridgeType == "bridge-claude2" {
		if cfg, cfgErr := config.Load(); cfgErr == nil && cfg.SubprocessTimeoutS > 0 {
			args = append(args, "-subprocess-timeout", fmt.Sprintf("%ds", cfg.SubprocessTimeoutS))
		}
	}

	proc := exec.Command(binary, args...)
	logOut, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		unlock()
		return fmt.Errorf("open log file: %w", err)
	}
	defer logOut.Close()

	proc.Stdout = logOut
	proc.Stderr = logOut

	// プロセスグループから切り離す (nohup 相当)
	setSysProcAttr(proc)

	fmt.Fprintf(os.Stderr, "starting @%s (type=%s, workdir=%s, log=%s)\n", participant, bridgeType, wd, logPath)

	if err := proc.Start(); err != nil {
		unlock()
		return fmt.Errorf("start bridge: %w", err)
	}

	pid := proc.Process.Pid

	// PID をロック保持中に即保存してから解放する
	st.Set(participant, pid, bridgeType, wd, tenant, logPath)
	if err := st.Save(); err != nil {
		unlock()
		return fmt.Errorf("save state: %w", err)
	}
	unlock()

	// ready シグナル待機（ロック解放後）
	rp := readyPatternFor(bridgeType)
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	ready := false

	for time.Now().Before(deadline) {
		if found, err := grepLog(logPath, rp); err == nil && found {
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !ready {
		// タイムアウト時もプロセスを残す（PID は保存済み）
		fmt.Fprintf(os.Stderr, "warning: timeout waiting for %q (pid=%d). Check log: %s\n", rp, pid, logPath)
	} else {
		fmt.Printf("ok pid=%d\n", pid)
	}

	return nil
}

// resolveBinary は bridgeType に対応するバイナリのパスを解決する。
// 優先順位: AGENT_HUB_{TYPE}_BIN env > PATH の {bridgeType}
// 例: bridge-claude2 → AGENT_HUB_BRIDGE_CLAUDE2_BIN
func resolveBinary(bridgeType string) (string, error) {
	typeUpper := strings.ToUpper(strings.ReplaceAll(bridgeType, "-", "_"))
	envVar := "AGENT_HUB_" + typeUpper + "_BIN"
	if binEnv := os.Getenv(envVar); binEnv != "" {
		if _, err := os.Stat(binEnv); err != nil {
			return "", fmt.Errorf("%s=%q not found: %w", envVar, binEnv, err)
		}
		return binEnv, nil
	}

	path, err := exec.LookPath(bridgeType)
	if err != nil {
		return "", fmt.Errorf("%s not found in PATH. Set %s or add %s to PATH", bridgeType, envVar, bridgeType)
	}
	return path, nil
}

// readyPatternFor は bridge type ごとの起動完了シグナル文字列を返す。
func readyPatternFor(bridgeType string) string {
	switch bridgeType {
	case "bridge-tmux":
		return "polling inbox"
	case "bridge-claude2":
		return "registered and listening"
	default:
		return "listening on inbox"
	}
}

// grepLog はログファイルに pattern が含まれているかチェックする。
func grepLog(logPath, pattern string) (bool, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if containsString(scanner.Text(), pattern) {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// pidString は int を文字列に変換するユーティリティ。
func pidString(pid int) string {
	return strconv.Itoa(pid)
}

// suppress unused warning
var _ = pidString

// pgrepHandle は "--participant <handle>" を cmdline に持つプロセスを pgrep で検索する。
// bridges.json に記録のない orphan プロセスの検出に使う (issue #14)。
// 見つかった場合は最初の PID を返す。見つからない / pgrep 非対応の場合は 0 を返す。
func pgrepHandle(handle string) (int, error) {
	pattern := "--participant " + handle
	out, err := exec.Command("pgrep", "-f", "--", pattern).Output()
	if err != nil {
		// exit code 1 = no match (success case)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return 0, nil
		}
		// pgrep 不在または予期しないエラー: チェックをスキップして続行
		return 0, nil
	}

	selfPID := os.Getpid()
	for _, field := range strings.Fields(strings.TrimSpace(string(out))) {
		pid, parseErr := strconv.Atoi(field)
		if parseErr != nil {
			continue
		}
		if pid == selfPID {
			// agenthubctl 自身も --participant <handle> を含むのでスキップ
			continue
		}
		return pid, nil
	}
	return 0, nil
}
