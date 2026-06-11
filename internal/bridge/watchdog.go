// watchdog.go — detect dead bridges and respawn them (issue #13, #12)
//
// 使い方:
//   one-shot (cron 向け):
//     agenthubctl watchdog
//   daemon (長期常駐):
//     agenthubctl watchdog --interval 30s [--notify-to @operator]
//
// one-shot はデーモンプロセス管理不要で cron に適す。
// daemon はシグナル (SIGINT/SIGTERM) で停止する。
package bridge

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/hub"
	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

// NewWatchdogCmd は `agenthubctl watchdog` サブコマンドを返す。
// --interval を指定するとデーモンモード、省略すると one-shot モードで動作する。
func NewWatchdogCmd() *cobra.Command {
	var intervalStr string
	var notifyTo string

	cmd := &cobra.Command{
		Use:   "watchdog",
		Short: "Detect dead bridges and respawn them (one-shot or daemon with --interval)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if intervalStr == "" {
				return RunWatchdog()
			}
			d, err := time.ParseDuration(intervalStr)
			if err != nil {
				return fmt.Errorf("invalid --interval %q: %w", intervalStr, err)
			}
			if d <= 0 {
				return fmt.Errorf("--interval must be positive")
			}
			notify := notifyTo
			if notify == "" {
				notify = os.Getenv("AGENT_HUB_OPERATOR")
			}
			return RunWatchdogDaemon(d, notify)
		},
	}
	cmd.Flags().StringVar(&intervalStr, "interval", "", "run as daemon with this check interval (e.g. 30s, 5m)")
	cmd.Flags().StringVar(&notifyTo, "notify-to", "", "send DM to this handle on respawn failure (or set AGENT_HUB_OPERATOR env)")
	return cmd
}

// deadEntries は state 内の死んでいる bridge エントリを Handle 昇順で返す。
// 出力・テストの決定性のためソートする。
func deadEntries(st *state.State) []state.Entry {
	var dead []state.Entry
	for _, e := range st.Bridges {
		if !e.IsRunning() {
			dead = append(dead, *e)
		}
	}
	sort.Slice(dead, func(i, j int) bool { return dead[i].Handle < dead[j].Handle })
	return dead
}

// respawnResult は 1 件の respawn 試行の結果。
type respawnResult struct {
	handle string
	err    error
}

// onceSummary は runWatchdogOnce の実行結果サマリ。
type onceSummary struct {
	failedHandles []string
}

// runWatchdogOnce は 1 回分の watchdog サイクルを実行する。
// 結果の詳細 (失敗ハンドル一覧) を onceSummary で返す。
//
// ロック設計の意図:
//   - 検出〜クリーンアップ（LoadLocked → 死活判定 → Delete → Save）はロック保持中に完了する。
//   - respawn（runSpawn）はロック解放後に実行する。runSpawn は内部で自前の LoadLocked() を
//     呼ぶため、ロックを保持したまま呼ぶとデッドロックする。
func runWatchdogOnce() (onceSummary, error) {
	st, unlock, err := state.LoadLocked()
	if err != nil {
		return onceSummary{}, fmt.Errorf("load state: %w", err)
	}

	total := len(st.Bridges)
	dead := deadEntries(st)

	if len(dead) == 0 {
		unlock()
		fmt.Printf("watchdog: all %d bridges alive\n", total)
		return onceSummary{}, nil
	}

	// 死んだエントリを state から削除し、ロック解放前に書き戻す。
	// これにより respawn 中に別の watchdog / spawn が走っても整合性が保たれる。
	for _, e := range dead {
		fmt.Printf("watchdog: @%s dead (pid=%d), respawning...\n", e.Handle, e.PID)
		st.Delete(e.Handle)
	}
	if err := st.Save(); err != nil {
		unlock()
		return onceSummary{}, fmt.Errorf("save state: %w", err)
	}
	unlock()

	alive := total - len(dead)

	// 並列 respawn。
	//
	// NOTE: display_name は state.Entry に保存していないため respawn では空になる（issue #23 で
	// 追加された --display-name は復元されない）。bridge は bootstrap で自身を register し直すため
	// 実用上は問題ないが、自動復元が必要なら Entry に DisplayName を追加する follow-up が要る。
	results := make([]respawnResult, len(dead))
	var wg sync.WaitGroup
	for i, e := range dead {
		wg.Add(1)
		go func(i int, e state.Entry) {
			defer wg.Done()
			err := runSpawn(e.Handle, e.BridgeType, e.Workdir, e.Tenant, "", defaultSpawnTimeoutS)
			results[i] = respawnResult{handle: e.Handle, err: err}
		}(i, e)
	}
	wg.Wait()

	var summary onceSummary
	respawned, failed := 0, 0
	for _, r := range results {
		if r.err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "watchdog: @%s respawn failed: %v\n", r.handle, r.err)
			summary.failedHandles = append(summary.failedHandles, r.handle)
			continue
		}
		respawned++
		if pid := currentPID(r.handle); pid > 0 {
			fmt.Printf("watchdog: @%s respawned (pid=%d)\n", r.handle, pid)
		} else {
			fmt.Printf("watchdog: @%s respawned\n", r.handle)
		}
	}

	if failed > 0 {
		fmt.Printf("watchdog: %d respawned, %d failed, %d alive\n", respawned, failed, alive)
	} else {
		fmt.Printf("watchdog: %d respawned, %d alive\n", respawned, alive)
	}
	return summary, nil
}

// RunWatchdog は one-shot で死んだ bridge を検出し respawn する。
func RunWatchdog() error {
	_, err := runWatchdogOnce()
	return err
}

// RunWatchdogDaemon は interval ごとに watchdog を繰り返し実行するデーモンループ。
// SIGINT / SIGTERM で正常終了する。
// respawn 失敗が出た場合、notifyTo が空でなければ DM で通知する。
func RunWatchdogDaemon(interval time.Duration, notifyTo string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWatchdogDaemonCtx(ctx, interval, notifyTo)
}

// runWatchdogDaemonCtx はコンテキスト制御可能なデーモンループ本体（テスト向け）。
func runWatchdogDaemonCtx(ctx context.Context, interval time.Duration, notifyTo string) error {
	fmt.Printf("watchdog daemon: starting (interval=%s)\n", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	runOnce := func() {
		summary, err := runWatchdogOnce()
		if err != nil {
			fmt.Fprintf(os.Stderr, "watchdog: %v\n", err)
			return
		}
		if len(summary.failedHandles) > 0 && notifyTo != "" {
			notifyRespawnFailures(summary.failedHandles, notifyTo)
		}
	}

	// 起動直後に 1 回実行してから ticker を待つ。
	runOnce()

	for {
		select {
		case <-ticker.C:
			runOnce()
		case <-ctx.Done():
			fmt.Println("watchdog daemon: shutting down")
			return nil
		}
	}
}

// notifyRespawnFailures は respawn 失敗ハンドルのリストを DM で通知する。
// hub への接続に失敗した場合は stderr に警告を出すのみで、呼び出し元には伝播しない。
func notifyRespawnFailures(handles []string, to string) {
	client, err := hub.NewClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: notify skipped (hub unavailable): %v\n", err)
		return
	}
	msg := fmt.Sprintf("watchdog: respawn failed for bridge(s): %s", strings.Join(handles, ", "))
	if _, err := client.CallTool("send_message", map[string]any{"to": to, "message": msg}); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: notify to %s failed: %v\n", to, err)
	}
}

// currentPID は respawn 後の handle の PID を state から読み直して返す。
// runSpawn が新 PID をロック保持中に保存済みのため、読み取り専用 Load で取得できる。
// 取得できない場合は 0 を返す（出力で pid を省略する）。
func currentPID(handle string) int {
	st, err := state.Load()
	if err != nil {
		return 0
	}
	if e := st.Get(handle); e != nil {
		return e.PID
	}
	return 0
}
