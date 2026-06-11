// watchdog.go — detect dead bridges and respawn them (issue #13)
//
// 想定運用: cron で 10 分おきに `agenthubctl watchdog` を one-shot 実行する。
//
//	*/10 * * * * agenthubctl watchdog
//
// 常駐 daemon ではなく one-shot にすることで、systemd / 監視プロセス自身の
// 死活管理を不要にしている。bridge が落ちても message は queue に溜まるだけなので
// 最大 10 分の dead time は許容できる、という前提に基づく。
package bridge

import (
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

// NewWatchdogCmd は `agenthubctl watchdog` サブコマンドを返す。
// cron エントリとして短く書けるよう root 直下に配置する（bridge サブコマンド配下ではない）。
func NewWatchdogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watchdog",
		Short: "Detect dead bridges and respawn them (intended for cron)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return RunWatchdog()
		},
	}
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

// RunWatchdog は死んだ bridge を検出し、state をクリーンアップして並列 respawn する。
//
// ロック設計の意図:
//   - 検出〜クリーンアップ（LoadLocked → 死活判定 → Delete → Save）はロック保持中に完了する。
//   - respawn（runSpawn）はロック解放後に実行する。runSpawn は内部で自前の LoadLocked() を
//     呼ぶため、ロックを保持したまま呼ぶとデッドロックする。
func RunWatchdog() error {
	st, unlock, err := state.LoadLocked()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	total := len(st.Bridges)
	dead := deadEntries(st)

	if len(dead) == 0 {
		unlock()
		fmt.Printf("watchdog: all %d bridges alive\n", total)
		return nil
	}

	// 死んだエントリを state から削除し、ロック解放前に書き戻す。
	// これにより respawn 中に別の watchdog / spawn が走っても整合性が保たれる。
	for _, e := range dead {
		fmt.Printf("watchdog: @%s dead (pid=%d), respawning...\n", e.Handle, e.PID)
		st.Delete(e.Handle)
	}
	if err := st.Save(); err != nil {
		unlock()
		return fmt.Errorf("save state: %w", err)
	}
	unlock()

	alive := total - len(dead)

	// 並列 respawn。runSpawn は最大 --timeout 秒（default 30s）の ready-wait を行うため、
	// bridge が多数死んでいる場合に sequential だと 10 分 cron と競合しうる。goroutine で並列化する。
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

	respawned, failed := 0, 0
	for _, r := range results {
		if r.err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "watchdog: @%s respawn failed: %v\n", r.handle, r.err)
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
	return nil
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
