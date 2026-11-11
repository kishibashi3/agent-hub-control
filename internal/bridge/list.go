// list.go — bridge list command
package bridge

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

func NewListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all known bridge workers (including untracked running processes)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList()
		},
	}
}

func runList() error {
	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// 実プロセスを正本にするため、state に無い稼働中 bridge を pgrep + /proc で発見する (issue #38)。
	orphans, oerr := findOrphanBridges(st)
	if oerr != nil {
		fmt.Fprintf(os.Stderr, "warning: orphan detection failed: %v\n", oerr)
	}

	if len(st.Bridges) == 0 && len(orphans) == 0 {
		fmt.Println("no bridges")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HANDLE\tSTATUS\tPID\tTYPE\tTENANT\tMODEL\tWORKDIR\tSTARTED")

	deadCount := 0
	var runningHandles []string // config 有無の検査対象 (issue #90)
	for _, e := range st.Bridges {
		status := "running"
		pid := e.PID
		if !e.IsRunning() {
			// 記録された PID は死んでいる。同一 handle が新しい PID で再起動していないか
			// 実プロセスを正本にして確認する (issue #38)。stop と同じ reconcile を list にも
			// 適用し、再起動済みなら live PID で running 表示する。これがないと stale entry の
			// ある handle は実稼働中でも dead 表示になり、list と stop の挙動が非対称になる。
			if livePID, perr := pgrepHandle(e.Handle); perr == nil && livePID != 0 {
				pid = livePID
			} else {
				status = "dead"
				deadCount++
			}
		}
		if status == "running" {
			runningHandles = append(runningHandles, e.Handle)
		}
		fmt.Fprintf(w, "@%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			e.Handle, status, pid, bridgeTypeOrDefault(e.BridgeType), tenantOrDefault(e.Tenant), modelDisplay(e.Model), e.Workdir, e.StartedAt)
	}

	// state に無い稼働中プロセスを untracked として併記する。
	for _, o := range orphans {
		runningHandles = append(runningHandles, o.handle)
		fmt.Fprintf(w, "@%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			o.handle, "untracked", o.pid, bridgeTypeOrDefault(o.bridgeType), tenantOrDefault(o.tenant), modelDisplay(o.model), o.workdir, "(running)")
	}

	if err := w.Flush(); err != nil {
		return err
	}

	// drift があれば reconcile/prune をサジェストする (issue #38 discoverability)。
	if len(orphans) > 0 {
		fmt.Fprintf(os.Stderr,
			"\nwarning: %d untracked bridge process(es) running but not in state. Run `agenthubctl bridge sync` to adopt them.\n",
			len(orphans))
	}
	if deadCount > 0 {
		fmt.Fprintf(os.Stderr,
			"warning: %d dead entr(ies) in state. Run `agenthubctl bridge prune` to remove them (or `bridge sync` for both).\n",
			deadCount)
	}

	// 稼働中なのに bridge config が無い handle は、死んだら fleet (`bridge start --all`) が起こさない (issue #90)。
	have, cerr := configuredHandles()
	if cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot read bridge configs: %v\n", cerr)
	} else {
		var noCfg []string
		for _, h := range runningHandles {
			if !have[h] {
				noCfg = append(noCfg, "@"+h)
			}
		}
		if len(noCfg) > 0 {
			sort.Strings(noCfg)
			fmt.Fprintf(os.Stderr,
				"warning: %d running bridge(s) have no saved config and will NOT be revived by fleet (`bridge start --all`) if they die: %s. Run `agenthubctl bridge sync` to save their config.\n",
				len(noCfg), strings.Join(noCfg, ", "))
		}
	}

	return nil
}

func tenantOrDefault(t string) string {
	if t == "" {
		return "(default)"
	}
	return t
}

func bridgeTypeOrDefault(t string) string {
	if t == "" {
		return defaultBridgeType
	}
	return t
}
