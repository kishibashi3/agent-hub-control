// restart.go — bridge restart command (stop → spawn with same workdir/tenant)
package bridge

import (
	"fmt"
	"os"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
	"github.com/kishibashi3/agent-hub-control/internal/config"
	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

// restartKillGrace は stop の待ちが --stop-timeout を超えて SIGKILL した後、プロセスが消えるのを待つ上限。
// 消える前に re-spawn すると spawn 側の実プロセスチェック (pgrep) で弾かれる。
const restartKillGrace = 5 * time.Second

// restartSpawn は re-spawn の実体。テストで差し替える (実 bridge binary を起動させないため)。
var restartSpawn = runSpawn

func NewRestartCmd() *cobra.Command {
	var (
		displayName string
		timeout     int
		stopTimeout time.Duration
		all         bool
	)

	cmd := &cobra.Command{
		Use:   "restart <handle> | --all",
		Short: "Restart a bridge worker (stop then spawn with same workdir/tenant)",
		Long: `Restart a bridge worker: stop it, then spawn it again with the same workdir/tenant.

The stop phase works like ` + "`bridge stop`" + `: it sends SIGTERM and waits for the bridge to
finish its graceful drain (/compact and processing of pending messages) and exit. SIGKILL is
sent only if the process is still alive after --stop-timeout. The default follows the bridge's
drain limit (compact 5m + subprocess timeout + 1m) plus a 1m margin: 37m with the default 30m
subprocess timeout. --all restarts bridges one at a time.

--timeout is the ready-signal wait after re-spawn (unchanged).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("stop-timeout") {
				cfg, err := config.Load()
				if err != nil {
					return fmt.Errorf("load config: %w", err)
				}
				stopTimeout = defaultStopTimeout(cfg.SubprocessTimeoutS)
			}
			if stopTimeout <= 0 {
				return fmt.Errorf("--stop-timeout must be positive (got %s)", stopTimeout)
			}
			if all {
				if len(args) > 0 {
					return fmt.Errorf("--all and <handle> are mutually exclusive")
				}
				return runRestartAll(timeout, stopTimeout)
			}
			if len(args) == 0 {
				return fmt.Errorf("either <handle> or --all is required")
			}
			handle := args[0]
			// Fall back to bridge config for display-name when not provided via flag.
			if !cmd.Flags().Changed("display-name") {
				if cfg, err := bridgecfg.Load(handle); err == nil && cfg != nil && cfg.DisplayName != "" {
					displayName = cfg.DisplayName
				}
			}
			return runRestart(handle, displayName, timeout, stopTimeout)
		},
	}

	cmd.Flags().StringVar(&displayName, "display-name", "", "display name passed to the bridge on re-spawn (optional)")
	cmd.Flags().IntVar(&timeout, "timeout", defaultSpawnTimeoutS, "seconds to wait for ready signal after re-spawn")
	cmd.Flags().DurationVar(&stopTimeout, "stop-timeout", 0, "how long to wait for the bridge to exit after SIGTERM before sending SIGKILL (default: bridge drain limit + 1m, same as bridge stop --timeout)")
	cmd.Flags().BoolVar(&all, "all", false, "Restart all known bridge workers sequentially")

	return cmd
}

func runRestart(handle, displayName string, spawnTimeoutS int, stopTimeout time.Duration) error {
	// ── 1. state を読み、エントリを取得 ──────────────────────────────────
	st, unlock, err := state.LoadLocked()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	entry := st.Get(handle)
	if entry == nil {
		unlock()
		return fmt.Errorf("@%s not found in state. Use `bridge list` to see running bridges.", handle)
	}

	// エントリから spawn に必要な情報を保存
	savedBridgeType := entry.BridgeType
	// model: display_name と同じく bridge config を再読し、保存されていればそれを優先する
	// (`config set X --model foo` → `restart X` で新 model が効く、PR #67 review Minor 1)。
	// config に無ければ state に記録された前回値を引き継ぐ (config 無しで spawn された bridge 用)。
	savedModel := modelSpec{ID: entry.Model, Source: modelSourceState}
	if cfg, cfgErr := bridgecfg.Load(handle); cfgErr == nil && cfg != nil && cfg.Model != "" {
		savedModel = modelSpec{ID: cfg.Model, Source: modelSourceConfig}
	}
	if savedBridgeType == "" {
		savedBridgeType = defaultBridgeType
	}
	savedWorkdir := entry.Workdir
	savedTenant := entry.Tenant
	savedPID := entry.PID
	isRunning := entry.IsRunning()

	// ── 2. stop ──────────────────────────────────────────────────────────
	if isRunning {
		// drain は最大で数十分かかるので lock を持ったまま待たない。state のエントリは旧プロセスが
		// 消えるまで残す (`bridge stop` と同じ)。待機中に Ctrl-C / SSH 切断 / SIGKILL などで restart が
		// 終わってもエントリ (workdir / tenant) が残るので、旧プロセス終了後に `bridge restart` を
		// もう一度実行すれば re-spawn できる (config のある bridge は fleet の `bridge start --all` も拾う)。
		// issue #97: 以前は先にエントリを消していたため、中断すると bridge が止まったまま state からも消えた。
		unlock()

		// stop と同じく SIGTERM → drain が終わるまで最大 stopTimeout 待ち → 超えたら SIGKILL (issue #92)。
		if stopErr := stopPID(handle, savedPID, stopTimeout); stopErr != nil {
			return fmt.Errorf("stop @%s: %w", handle, stopErr)
		}
		// SIGKILL に落ちた場合はプロセスが消えるまで少し待つ (消える前の re-spawn は pgrep で弾かれる)。
		// 消えなければエントリは残したままエラーで終わる。
		if proc, findErr := os.FindProcess(savedPID); findErr == nil && !waitExit(proc, restartKillGrace, nil) {
			return fmt.Errorf("@%s (pid=%d) still alive %s after SIGKILL; not re-spawning", handle, savedPID, restartKillGrace)
		}
		fmt.Fprintf(os.Stderr, "stopped @%s (pid=%d)\n", handle, savedPID)

		// lock を取り直し、止めたプロセスのエントリが残っているときだけ消して re-spawn する。
		// 旧プロセスの exit から lock を取り直すまでの間に別の spawn (fleet の `bridge start --all` 等) が
		// 先に re-spawn していた場合 (エントリが消えている / 別 PID に変わっている) は二重 spawn を避けて
		// エラーで終わる。この場合 bridge はその spawn が起動したもので動いている (issue #97 で許容)。
		st, unlock, err = state.LoadLocked()
		if err != nil {
			return fmt.Errorf("load state: %w", err)
		}
		cur := st.Get(handle)
		if cur == nil {
			// stop / prune / sync などがエントリを消した。bridge は止まったままで state にも無い。
			unlock()
			return fmt.Errorf("@%s state entry was removed while waiting for pid=%d to exit (by `bridge stop` / prune / sync); bridge is stopped and not re-spawned. Start it again with `bridge start`", handle, savedPID)
		}
		if cur.PID != savedPID {
			unlock()
			return fmt.Errorf("@%s state changed while waiting for pid=%d to exit (likely re-spawned by fleet `bridge start --all` or another spawn); not re-spawning. Check with `bridge list`", handle, savedPID)
		}
		st.Delete(handle)
		if saveErr := st.Save(); saveErr != nil {
			unlock()
			return fmt.Errorf("save state: %w", saveErr)
		}
		unlock()
	} else {
		fmt.Fprintf(os.Stderr, "warning: @%s (pid=%d) was not running. Cleaning up state and re-spawning.\n", handle, savedPID)
		st.Delete(handle)
		if saveErr := st.Save(); saveErr != nil {
			unlock()
			return fmt.Errorf("save state: %w", saveErr)
		}
		unlock()
	}

	// ── 3. spawn ─────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "re-spawning @%s (type=%s, workdir=%s)...\n", handle, savedBridgeType, savedWorkdir)
	return restartSpawn(handle, savedBridgeType, savedWorkdir, savedTenant, displayName, savedModel, spawnTimeoutS)
}

// runRestartAll は既知の bridge を 1 体ずつ順に restart する (並列化はしない、issue #92)。
func runRestartAll(spawnTimeoutS int, stopTimeout time.Duration) error {
	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if len(st.Bridges) == 0 {
		fmt.Println("no bridges to restart")
		return nil
	}

	var handles []string
	for h := range st.Bridges {
		handles = append(handles, h)
	}

	var failed []string
	for _, h := range handles {
		dn := ""
		if cfg, loadErr := bridgecfg.Load(h); loadErr == nil && cfg != nil {
			dn = cfg.DisplayName
		}
		fmt.Fprintf(os.Stderr, "=== restarting @%s ===\n", h)
		if err := runRestart(h, dn, spawnTimeoutS, stopTimeout); err != nil {
			fmt.Fprintf(os.Stderr, "error: @%s: %v\n", h, err)
			failed = append(failed, h)
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("restart failed for: %v", failed)
	}
	fmt.Printf("restarted %d bridge(s)\n", len(handles))
	return nil
}
