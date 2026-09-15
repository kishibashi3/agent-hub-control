// spawn.go — bridge spawn command
package bridge

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
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
		workdir     string
		tenant      string
		bridgeType  string
		timeout     int
		displayName string
	)

	cmd := &cobra.Command{
		Use:   "spawn [<handle>]",
		Short: "Spawn a bridge worker",
		// Accept 0 args (--participant flag) or 1 positional arg (handle).
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if u, _ := cmd.Flags().GetString("user"); u != "" {
				return fmt.Errorf("--user は廃止されました。--participant / -p を使用してください。")
			}

			// Resolve handle: positional arg > --participant flag.
			participant, _ := cmd.Flags().GetString("participant")
			if len(args) > 0 {
				participant = args[0]
			}
			if participant == "" {
				return fmt.Errorf("handle is required (positional arg or --participant)")
			}

			// Apply saved config as defaults for unset flags.
			cfg, err := bridgecfg.Load(participant)
			if err != nil {
				return fmt.Errorf("load bridge config: %w", err)
			}
			if cfg != nil {
				if workdir == "" {
					workdir = cfg.Workdir
				}
				if !cmd.Flags().Changed("tenant") && tenant == "" {
					tenant = cfg.Tenant
				}
				if !cmd.Flags().Changed("type") && bridgeType == defaultBridgeType && cfg.BridgeType != "" {
					bridgeType = cfg.BridgeType
				}
				if displayName == "" {
					displayName = cfg.DisplayName
				}
			}

			return runSpawn(participant, bridgeType, workdir, tenant, displayName, timeout)
		},
	}

	cmd.Flags().StringP("participant", "p", "", "agent-hub handle (without @)")
	cmd.Flags().StringP("user", "u", "", "")
	_ = cmd.Flags().MarkHidden("user")
	cmd.Flags().StringVarP(&workdir, "workdir", "w", "", "peer workdir with CLAUDE.md (overrides saved config)")
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant ID (overrides saved config and AGENT_HUB_TENANT env)")
	cmd.Flags().StringVar(&bridgeType, "type", defaultBridgeType, "bridge type (bridge-claude2, bridge-codex2, bridge-gemini, …)")
	cmd.Flags().IntVar(&timeout, "timeout", defaultSpawnTimeoutS, "seconds to wait for ready signal")
	cmd.Flags().StringVar(&displayName, "display-name", "", "display name passed to the bridge for register (falls back to bridge config)")

	return cmd
}

var validHandle = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func runSpawn(participant, bridgeType, workdir, tenantFlag, displayName string, timeoutS int) error {
	if !validHandle.MatchString(participant) {
		return fmt.Errorf("invalid handle %q: only [a-zA-Z0-9_-] allowed", participant)
	}

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
		cfg, cfgErr := config.Load()
		if cfgErr != nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", cfgErr)
			os.Exit(1)
		}
		if cfg.SubprocessTimeoutS > 0 {
			args = append(args, "-subprocess-timeout", fmt.Sprintf("%ds", cfg.SubprocessTimeoutS))
		}
		if displayName != "" {
			args = append(args, "-display-name", displayName)
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
		// タイムアウト: プロセスを kill して state を巻き戻す
		if p, findErr := os.FindProcess(pid); findErr == nil {
			_ = p.Signal(syscall.SIGTERM)
			time.Sleep(500 * time.Millisecond)
			_ = p.Kill()
		}
		if rollbackSt, rollbackUnlock, lockErr := state.LoadLocked(); lockErr == nil {
			rollbackSt.Delete(participant)
			_ = rollbackSt.Save()
			rollbackUnlock()
		}
		return fmt.Errorf("timeout waiting for @%s to become ready (pid=%d killed). Check log: %s", participant, pid, logPath)
	}

	fmt.Printf("ok pid=%d\n", pid)
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
		if err := checkBridgeBinaryName(binEnv); err != nil {
			return "", fmt.Errorf("%s=%q: %w", envVar, binEnv, err)
		}
		return binEnv, nil
	}

	path, err := exec.LookPath(bridgeType)
	if err != nil {
		return "", fmt.Errorf("%s not found in PATH. Set %s or add %s to PATH", bridgeType, envVar, bridgeType)
	}
	if err := checkBridgeBinaryName(path); err != nil {
		return "", fmt.Errorf("%s resolved to %q: %w", bridgeType, path, err)
	}
	return path, nil
}

// checkBridgeBinaryName は spawn するバイナリが IsBridgeProcess (argv[0] + /proc/<pid>/exe) の
// 両方の判定を通る名前かを、spawn 前に検査する。argv[0] には与えられたパス p がそのまま入るが、
// kernel が exe として報告するのは symlink を解決した実体なので、両方の basename を見る
// (issue #50 review Minor 1: `bridge-x → symlink → bridge` は argv 判定だけ通り、起動後に恒久 dead
// 判定されて watchdog が重複 spawn する)。
func checkBridgeBinaryName(p string) error {
	if !state.LooksLikeBridgeExe(p) {
		return errors.New(bridgeBinaryNameHint)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}
	if !state.LooksLikeBridgeExe(real) {
		return fmt.Errorf("symlink target %q: %s", real, bridgeBinaryNameHint)
	}
	return nil
}

// bridgeBinaryNameHint は bridge バイナリの命名不変条件を破ったときのエラー文。
// IsRunning / pgrepHandle は argv[0] と /proc/<pid>/exe の basename が "bridge-" で始まることで
// 本物の bridge を識別する (issue #47 / #50)。この不変条件を満たさないバイナリを spawn すると、
// 起動直後から恒久的に dead 判定され fleet watchdog が毎 tick 重複 spawn するため、spawn 時点で拒否する。
// exe は symlink 解決後の実ファイルを指すため、symlink や wrapper の名前を変えても通らない —
// 実体 (ELF) 自体を "bridge-*" に rename する必要がある。
const bridgeBinaryNameHint = "bridge binary basename must start with \"bridge-\" (process identification relies on it; rename the real binary itself — a symlink or wrapper named bridge-* is not enough because /proc/<pid>/exe resolves to the target)"

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

// pgrepHandle は "--participant <handle>" または "--user <handle>" を cmdline に持つプロセスを
// pgrep で検索する。bridges.json に記録のない orphan プロセスの検出に使う (issue #14)。
// --user は旧フラグ名（v0.3.0 以前）。旧バイナリで起動した orphan も検出できるよう両方を確認する。
// 見つかった場合は最初の PID を返す。見つからない / pgrep 非対応の場合は 0 を返す。
//
// pgrep -f は cmdline 文字列に当該パターンを含む「任意の」プロセスにマッチするため、
// agenthubctl 自身や "bash -c '... --participant <handle> ...'" のラッパーシェルにも
// 誤マッチする (issue #31)。selfPID 除外だけでは親 bash ラッパーや別 invocation の
// agenthubctl を除外できないので、候補ごとに実 argv を /proc から読み直し、
// 本物の bridge ワーカーであることを looksLikeBridgeProcess で確認してから返す。
func pgrepHandle(handle string) (int, error) {
	selfPID := os.Getpid()
	for _, flag := range []string{"--participant", "--user"} {
		pattern := flag + " " + handle
		out, err := exec.Command("pgrep", "-f", "--", pattern).Output()
		if err != nil {
			// exit code 1 = no match (success case)
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				continue
			}
			// pgrep 不在または予期しないエラー: チェックをスキップして続行
			return 0, nil
		}

		for _, field := range strings.Fields(strings.TrimSpace(string(out))) {
			pid, parseErr := strconv.Atoi(field)
			if parseErr != nil {
				continue
			}
			if pid == selfPID {
				// agenthubctl 自身も --participant <handle> を含むのでスキップ
				continue
			}
			// pgrep の substring マッチを実 argv で検証する。これにより
			//   - 親 "bash -c '... --participant <handle> ...'" ラッパー (argv[0]=bash)
			//   - 別 invocation の agenthubctl (argv[0]=agenthubctl, --participant は
			//     spawn サブコマンドの引数であって bridge バイナリの引数ではない)
			// を除外し、本物の orphan bridge だけを検出する (issue #31)。argv が通っても
			// /proc/<pid>/exe が bridge バイナリでなければ偽装として除外する (issue #50)。
			if ok, err := state.IsBridgeProcess(pid, handle); err != nil || !ok {
				continue
			}
			return pid, nil
		}
	}
	return 0, nil
}

// looksLikeBridgeProcess は state package に移設した argv 判定の薄い wrapper (issue #47)。
// 実プロセス判定は state.IsBridgeProcess (argv + exe) を直接使う (issue #50)。
func looksLikeBridgeProcess(argv []string, handle string) bool {
	return state.LooksLikeBridgeProcess(argv, handle)
}
