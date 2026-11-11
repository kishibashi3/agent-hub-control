// sync.go — bridge sync command
//
// 双方向同期: bridges.json と実プロセスを突き合わせて整合させる
//   - dead entry の削除（prune と同様）
//   - bridges.json に存在しない実行中の bridge-claude2 プロセスを検出して entry を追加
//   - 稼働中 (state 上の running / 取り込んだ orphan) で bridge config が無い handle に config を作る
//     (issue #90: config が無いと fleet の `start --all` が死んだ bridge を起こさない)
package bridge

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

func NewSyncCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync bridges.json with actual processes (prune dead + register orphans) and save missing bridge configs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSync(dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would change without modifying bridges.json or bridge configs")
	return cmd
}

// orphanEntry holds info parsed from a running process not in bridges.json.
type orphanEntry struct {
	pid        int
	handle     string
	workdir    string
	tenant     string
	bridgeType string
	model      string
}

func runSync(dryRun bool) error {
	st, unlock, err := state.LoadLocked()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	defer unlock()

	// ── 1. dead entry を収集 ────────────────────────────────────────────
	var dead []string
	for handle, entry := range st.Bridges {
		if !entry.IsRunning() {
			dead = append(dead, handle)
		}
	}

	// ── 2. orphan プロセスを検出 ─────────────────────────────────────────
	// bridges.json に記録のない bridge-claude2 プロセスを pgrep + /proc で特定する
	orphans, err := findOrphanBridges(st)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: orphan detection failed: %v\n", err)
		// 検出に失敗してもdead entryの削除は続行する
	}

	// ── 2b. config の無い稼働中 handle を収集 (issue #90) ────────────────
	missingCfg, err := missingConfigs(st, orphans)
	if err != nil {
		return fmt.Errorf("check bridge configs: %w", err)
	}

	// ── 3. 変更内容を表示 ─────────────────────────────────────────────
	changed := len(dead) > 0 || len(orphans) > 0 || len(missingCfg) > 0

	if !changed {
		fmt.Println("bridges.json and bridge configs are in sync with running processes")
		return nil
	}

	if len(dead) > 0 {
		for _, h := range dead {
			pid := st.Bridges[h].PID
			if dryRun {
				fmt.Printf("would remove dead @%s (pid=%d)\n", h, pid)
			} else {
				fmt.Printf("removing dead @%s (pid=%d)\n", h, pid)
				st.Delete(h)
			}
		}
	}

	if len(orphans) > 0 {
		for _, o := range orphans {
			if dryRun {
				fmt.Printf("would add orphan @%s (pid=%d, workdir=%s)\n", o.handle, o.pid, o.workdir)
			} else {
				fmt.Printf("adding orphan @%s (pid=%d, workdir=%s)\n", o.handle, o.pid, o.workdir)
				logPath, err := resolveOrphanLogPath(o.handle)
				if err != nil {
					return fmt.Errorf("resolve log path for orphan @%s: %w", o.handle, err)
				}
				st.Bridges[o.handle] = &state.Entry{
					Handle:     o.handle,
					PID:        o.pid,
					BridgeType: o.bridgeType,
					Workdir:    o.workdir,
					Tenant:     o.tenant,
					LogPath:    logPath,
					StartedAt:  time.Now().UTC().Format(time.RFC3339),
					Model:      o.model,
				}
				if o.model != "" {
					st.Bridges[o.handle].ModelSource = modelSourceCmdline
				}
			}
		}
	}

	if dryRun {
		for _, c := range missingCfg {
			fmt.Printf("would save bridge config for running @%s (workdir=%s)\n", c.Handle, c.Workdir)
		}
		return nil
	}
	if err := st.Save(); err != nil {
		return err
	}

	var cfgFailed []string
	for _, c := range missingCfg {
		created, err := ensureConfig(c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: @%s: cannot save bridge config: %v\n", c.Handle, err)
			cfgFailed = append(cfgFailed, c.Handle)
			continue
		}
		if created {
			fmt.Printf("saved bridge config for running @%s (workdir=%s)\n", c.Handle, c.Workdir)
		}
	}
	if len(cfgFailed) > 0 {
		return fmt.Errorf("bridge config not saved for %v: fleet `bridge start --all` will not revive them", cfgFailed)
	}
	return nil
}

// missingConfigs は稼働中 (state 上で running の entry と、これから取り込む orphan) のうち
// bridge config が無い handle について、起動中プロセスの実パラメータから作る config を返す (issue #90)。
// Handle 昇順。dead entry は対象外 (止まっているものを fleet に起こさせる宣言はしない)。
func missingConfigs(st *state.State, orphans []orphanEntry) ([]bridgecfg.BridgeConfig, error) {
	have, err := configuredHandles()
	if err != nil {
		return nil, err
	}
	var out []bridgecfg.BridgeConfig
	for h, e := range st.Bridges {
		if have[h] || !e.IsRunning() {
			continue
		}
		out = append(out, bridgecfg.BridgeConfig{
			Handle: h, Workdir: e.Workdir, Tenant: e.Tenant, BridgeType: e.BridgeType,
			DisplayName: cmdlineDisplayName(e.PID), Model: e.Model,
		})
	}
	for _, o := range orphans {
		if have[o.handle] {
			continue
		}
		out = append(out, bridgecfg.BridgeConfig{
			Handle: o.handle, Workdir: o.workdir, Tenant: o.tenant, BridgeType: o.bridgeType,
			DisplayName: cmdlineDisplayName(o.pid), Model: o.model,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out, nil
}

// resolveOrphanLogPath は sync が取り込む orphan の log_path を決める (issue #54)。
// orphan は agenthubctl 以外 (旧バイナリ・手動起動) で spawn されている可能性があるので、
// 新パスが存在すればそれ、無ければ旧 /tmp パスが regular file として存在すればそれ (deprecation
// warning)、どちらも無ければ新パスを記録する (`bridge logs` は stat エラーで場所を明示する)。
// 旧パスは Lstat で見て symlink または他者所有の regular file なら採用しない (/tmp 上の他者のファイルを
// bridges.json に永続化して `bridge logs` / `status` に表示させない。spawn 側 linkLegacyLogPath と対称、
// issue #72)。
func resolveOrphanLogPath(handle string) (string, error) {
	newPath, err := state.BridgeLogPath(handle)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(newPath); err == nil {
		return newPath, nil
	}
	legacy := state.LegacyBridgeLogPath(legacyLogDir, handle)
	if fi, err := os.Lstat(legacy); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(os.Stderr, "warning: @%s: ignoring deprecated log path %s because it is a symlink; recording %s\n", handle, legacy, newPath)
			return newPath, nil
		}
		if !ownedBySelf(fi) {
			fmt.Fprintf(os.Stderr, "warning: @%s: ignoring deprecated log path %s because it is not owned by us; recording %s\n", handle, legacy, newPath)
			return newPath, nil
		}
		fmt.Fprintf(os.Stderr, "warning: @%s: adopting deprecated log path %s (restart the bridge to move it under AGENT_HUB_HOME/logs)\n", handle, legacy)
		return legacy, nil
	}
	return newPath, nil
}

// findOrphanBridges は bridges.json に存在しない bridge-claude2 プロセスを返す。
func findOrphanBridges(st *state.State) ([]orphanEntry, error) {
	// pgrep で bridge-claude2 の全 PID を取得
	pids, err := pgrepBridgeProcesses()
	if err != nil {
		return nil, err
	}

	selfPID := os.Getpid()
	var orphans []orphanEntry

	for _, pid := range pids {
		if pid == selfPID {
			continue
		}

		// /proc/<pid>/cmdline を読んでフラグを解析
		cmdlineBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue // プロセスが終了した可能性
		}

		args := strings.Split(string(cmdlineBytes), "\x00")
		handle, workdir, tenant, bridgeType, model := parseBridgeCmdline(args)
		if handle == "" {
			continue // --participant が見つからない
		}
		if !validHandle.MatchString(handle) {
			// cmdline は他者プロセス由来なので検証する。不正な handle は log_path (AGENT_HUB_HOME/logs 配下) の
			// パス要素になり、"bridge-../.." で logs 外を指しうる (PR #71 review Minor 3)。
			fmt.Fprintf(os.Stderr, "warning: skipping orphan pid=%d: invalid handle %q\n", pid, handle)
			continue
		}

		// すでに bridges.json に記録されているか確認
		if entry := st.Get(handle); entry != nil {
			continue
		}

		orphans = append(orphans, orphanEntry{
			pid:        pid,
			handle:     handle,
			workdir:    workdir,
			tenant:     tenant,
			bridgeType: bridgeType,
			model:      model,
		})
	}

	return orphans, nil
}

// pgrepBridgeProcesses は bridge-claude2 または bridge-* プロセスの PID 一覧を返す。
func pgrepBridgeProcesses() ([]int, error) {
	// /proc を直接スキャンして bridge- で始まるプロセスを検出する
	// pgrep への依存を避け、bridge-claude2 以外の bridge type も対象にする
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("readdir /proc: %w", err)
	}

	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // 数字でないエントリはスキップ
		}

		commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(commBytes))
		if strings.HasPrefix(comm, "bridge-") {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// parseBridgeCmdline は bridge プロセスの cmdline 引数から各フラグを抽出する。
// args は /proc/<pid>/cmdline を "\x00" で split したもの。
func parseBridgeCmdline(args []string) (handle, workdir, tenant, bridgeType, model string) {
	if len(args) > 0 {
		comm := args[0]
		if idx := strings.LastIndex(comm, "/"); idx >= 0 {
			comm = comm[idx+1:]
		}
		if strings.HasPrefix(comm, "bridge-") {
			bridgeType = comm
		}
	}

	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--participant":
			handle = args[i+1]
		case "--workdir":
			workdir = args[i+1]
		case "--tenant":
			tenant = args[i+1]
		case "-model", "--model":
			// spawn は "-model <id>" で渡す (Go flag は -/-- 同義)。orphan 取り込みで Entry.Model を復元する (issue #46)。
			model = args[i+1]
		}
	}
	return
}
