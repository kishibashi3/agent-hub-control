// start.go — bridge start command (config-driven spawn, supports --all)
package bridge

import (
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
	return runSpawn(handle, bridgeType, cfg.Workdir, cfg.Tenant, cfg.DisplayName, timeout)
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
		if err := runSpawn(cfg.Handle, bridgeType, cfg.Workdir, cfg.Tenant, cfg.DisplayName, timeout); err != nil {
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
