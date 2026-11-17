// start.go — bridge start command (config-driven spawn, supports --all)
package bridge

import (
	"errors"
	"fmt"
	"os"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

func NewStartCmd() *cobra.Command {
	var (
		all     bool
		timeout int
	)

	cmd := &cobra.Command{
		Use:   "start <handle> | --all",
		Short: "Start bridge worker(s) from config, skip already-running ones",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) > 0 {
					return fmt.Errorf("--all and <handle> are mutually exclusive")
				}
				return runStartAll(timeout)
			}
			if len(args) == 0 {
				return fmt.Errorf("either <handle> or --all is required")
			}
			return runStartOne(args[0], timeout)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Start all configured bridge workers (skip already-running)")
	cmd.Flags().IntVar(&timeout, "timeout", defaultSpawnTimeoutS, "seconds to wait for each bridge ready signal")
	return cmd
}

func runStartOne(handle string, timeout int) error {
	cfg, err := bridgecfg.Load(handle)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("no config saved for @%s. Run `bridge config set %s --workdir <path>` first.", handle, handle)
	}
	bridgeType := cfg.BridgeType
	if bridgeType == "" {
		bridgeType = defaultBridgeType
	}
	// stopped_at は runSpawn が running チェックを通った後に消す (issue #98, PR #102 review Minor 2)。
	return runSpawn(handle, bridgeType, cfg.Workdir, cfg.Tenant, cfg.DisplayName, modelSpec{ID: cfg.Model, Source: modelSourceConfig}, configBin(cfg), timeout, stoppedClear)
}

func runStartAll(timeout int) error {
	cfgs, err := bridgecfg.ListAll()
	if err != nil {
		return fmt.Errorf("list configs: %w", err)
	}
	if len(cfgs) == 0 {
		fmt.Println("no bridge configs saved. Use `bridge config set` to register bridges.")
		return nil
	}

	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	var started, skipped int
	var failed []string
	for _, cfg := range cfgs {
		// `bridge stop` で止めてある bridge は起こさない (issue #98)。クラッシュで死んだものには
		// stopped_at が無いので、これまで通り起こす。ここは早めの skip で、ループ中に stop された場合は
		// runSpawn (stoppedSkip) が state lock を持ったまま config を読み直して弾く。
		if cfg.Stopped() {
			fmt.Fprintf(os.Stderr, "skipping @%s (stopped since %s)\n", cfg.Handle, cfg.StoppedAt)
			skipped++
			continue
		}
		entry := st.Get(cfg.Handle)
		if entry != nil && entry.IsRunning() {
			fmt.Fprintf(os.Stderr, "skipping @%s (already running pid=%d)\n", cfg.Handle, entry.PID)
			skipped++
			continue
		}
		// Also check for orphan processes not in state.
		if pid, err := pgrepHandle(cfg.Handle); err == nil && pid != 0 {
			fmt.Fprintf(os.Stderr, "skipping @%s (already running pid=%d, not in state)\n", cfg.Handle, pid)
			skipped++
			continue
		}

		bridgeType := cfg.BridgeType
		if bridgeType == "" {
			bridgeType = defaultBridgeType
		}
		fmt.Fprintf(os.Stderr, "starting @%s (type=%s, workdir=%s)...\n", cfg.Handle, bridgeType, cfg.Workdir)
		err := runSpawn(cfg.Handle, bridgeType, cfg.Workdir, cfg.Tenant, cfg.DisplayName, modelSpec{ID: cfg.Model, Source: modelSourceConfig}, configBin(cfg), timeout, stoppedSkip)
		var stoppedErr *errStoppedInConfig
		if errors.As(err, &stoppedErr) {
			fmt.Fprintf(os.Stderr, "skipping @%s (%v)\n", cfg.Handle, stoppedErr)
			skipped++
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: @%s: %v\n", cfg.Handle, err)
			failed = append(failed, cfg.Handle)
			continue
		}
		started++
	}

	if len(failed) > 0 {
		return fmt.Errorf("start failed for: %v", failed)
	}
	fmt.Printf("done: %d started, %d skipped\n", started, skipped)
	return nil
}

// clearStoppedForExplicitStart は明示的な `bridge start <handle>` / `bridge spawn` で config の
// stopped_at を消す (明示的に起こした = 動かす、issue #98)。runSpawn (stoppedClear) が running チェックを
// 通った後・プロセスを起こす前に呼ぶ。起動の成否より前に消すのは、起こすという意図を記録するため (起動に
// 失敗しても fleet が次回また起こしにいく = stopped_at の無い config と同じ扱い)。
// config が無い / stopped_at が無い場合は config に触らない。
func clearStoppedForExplicitStart(handle string) error {
	cleared, err := bridgecfg.ClearStopped(handle)
	if err != nil {
		return fmt.Errorf("clear stopped_at in bridge config: %w", err)
	}
	if cleared {
		fmt.Fprintf(os.Stderr, "note: cleared stopped_at for @%s (fleet will start it again)\n", handle)
	}
	return nil
}

// configBin は bridge config の bin を固定指定にする (start / start --all、issue #88)。
func configBin(cfg *bridgecfg.BridgeConfig) binSpec {
	if cfg == nil || cfg.Bin == "" {
		return binSpec{}
	}
	return binSpec{Name: cfg.Bin, Source: binSourceConfig}
}
