// stop.go — bridge stop command
package bridge

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/config"
	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

// bridge-claude2 の graceful drain 上限 (bridges cmd/bridge/worker.go runGracefulDrain) に合わせた
// stop の既定待ち時間の内訳 (issue #89)。drain の上限は compact 5m + SubprocessTimeout + 1m で、
// bridge-codex2 (SubprocessTimeout + 1m) はこれ以下に収まる。
const (
	bridgeCompactTimeout           = 5 * time.Minute
	bridgeDefaultSubprocessTimeout = 30 * time.Minute // bridge-claude2 の -subprocess-timeout 既定値
	bridgeDrainSlack               = time.Minute      // drain 上限に bridge 側が足している 1m
	// stopExitMargin は drain 上限を超えて待つ余裕。drain 前の in-flight 便の中断と、"stopped" ログの後の
	// telemetry flush (OTLP 先が応答しないと最大約 30 秒、issue #89 で実測) を吸収する。
	stopExitMargin = time.Minute
	stopPollEvery  = 200 * time.Millisecond
	// stopProgressEvery は待機中に「まだ drain 中」と知らせる間隔。
	stopProgressEvery = 30 * time.Second
)

// defaultStopTimeout は SIGKILL に落とすまでの既定待ち時間 = bridge の drain 上限 + 余裕。
// agenthubctl config の subprocess_timeout_s は spawn 時に bridge へ渡しているのでそれに揃える
// (0 = bridge 既定 30m)。bridge 側で env (AGENT_HUB_SUBPROCESS_TIMEOUT) だけで変えている場合は
// 追従できないので --timeout で指定する。
func defaultStopTimeout(subprocessTimeoutS int) time.Duration {
	sub := bridgeDefaultSubprocessTimeout
	if subprocessTimeoutS > 0 {
		sub = time.Duration(subprocessTimeoutS) * time.Second
	}
	return bridgeCompactTimeout + sub + bridgeDrainSlack + stopExitMargin
}

func NewStopCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "stop <handle>",
		Short: "Stop a running bridge worker (finds the real process even if state is stale)",
		Long: `Stop a running bridge worker.

Sends SIGTERM and waits for the bridge to finish its graceful drain (/compact and
processing of pending messages) and exit. SIGKILL is sent only if the process is
still alive after --timeout. The default timeout follows the bridge's drain limit
(compact 5m + subprocess timeout + 1m) plus a 1m margin: 37m with the default 30m
subprocess timeout. Pass a short --timeout (e.g. 5s) to cut the drain short.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("timeout") {
				cfg, err := config.Load()
				if err != nil {
					return fmt.Errorf("load config: %w", err)
				}
				timeout = defaultStopTimeout(cfg.SubprocessTimeoutS)
			}
			if timeout <= 0 {
				return fmt.Errorf("--timeout must be positive (got %s)", timeout)
			}
			return runStop(args[0], timeout)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "how long to wait for the bridge to exit after SIGTERM before sending SIGKILL (default: bridge drain limit + 1m)")
	return cmd
}

func runStop(user string, timeout time.Duration) error {
	st, unlock, err := state.LoadLocked()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()

	entry := st.Get(user)

	// 実プロセスを正本にする (issue #38)。state が stale でも / そもそも無くても、
	// 実際に動いている bridge プロセスを発見して止める (reconcile してから stop)。
	livePID := 0
	switch {
	case entry != nil && entry.IsRunning():
		livePID = entry.PID
	default:
		// state に無い、または state の PID が既に死んでいる場合は実プロセスを探す。
		pid, perr := pgrepHandle(user)
		if perr != nil {
			return fmt.Errorf("search running process for @%s: %w", user, perr)
		}
		livePID = pid
		if pid != 0 && entry == nil {
			fmt.Fprintf(os.Stderr, "note: @%s is not in state but found running (pid=%d) — stopping untracked process.\n", user, pid)
		} else if pid != 0 && entry != nil {
			fmt.Fprintf(os.Stderr, "note: state for @%s was stale (recorded pid=%d dead); stopping live process (pid=%d).\n", user, entry.PID, pid)
		}
	}

	if livePID == 0 {
		if entry != nil {
			fmt.Fprintf(os.Stderr, "warning: @%s (pid=%d) is not running. Cleaning up stale state.\n", user, entry.PID)
			st.Delete(user)
			return st.Save()
		}
		return fmt.Errorf("@%s not found in state or running processes. Use `bridge list` to see what is running.", user)
	}

	// drain は最大で数十分かかるので、待っている間は state lock を手放す (他の agenthubctl を
	// 止めない)。state のエントリはプロセスが消えるまで残し、`bridge list` で running に見えるようにする。
	// 待機中に同じ handle を spawn しても、実プロセス (pgrep) が生きている間は spawn 側が弾く。
	recordedPID := 0
	if entry != nil {
		recordedPID = entry.PID
	}
	unlock()
	locked = false

	if err := stopPID(user, livePID, timeout); err != nil {
		return err
	}

	fmt.Printf("stopped @%s (pid=%d)\n", user, livePID)

	st, unlock, err = state.LoadLocked()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	locked = true
	// 止めたプロセス (または stop 開始時に記録されていた stale な PID) のエントリだけを消す。
	// 待っている間に別の spawn が同じ handle で新しいプロセスを登録していたら消さない。
	if e := st.Get(user); e != nil && (e.PID == livePID || e.PID == recordedPID) {
		st.Delete(user)
		if err := st.Save(); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}

	return nil
}

// stopPID は pid に SIGTERM を送り、プロセスが終わるのを最大 timeout 待つ。それでも生きていれば
// SIGKILL する (issue #89: 以前は 5 秒固定で、bridge の graceful drain を打ち切っていた)。
func stopPID(user string, pid int, timeout time.Duration) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process pid=%d: %w", pid, err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("SIGTERM pid=%d: %w", pid, err)
	}

	if timeout > 5*time.Second {
		fmt.Fprintf(os.Stderr, "stopping @%s (pid=%d): waiting up to %s for graceful drain (Ctrl-C leaves it draining)...\n", user, pid, timeout)
	}
	if waitExit(proc, timeout, func(elapsed time.Duration) {
		fmt.Fprintf(os.Stderr, "  still draining @%s (pid=%d, %s elapsed)\n", user, pid, elapsed.Round(time.Second))
	}) {
		return nil
	}

	fmt.Fprintf(os.Stderr, "warning: pid=%d did not exit within %s after SIGTERM, sending SIGKILL\n", pid, timeout)
	_ = proc.Kill()
	return nil
}

// waitExit は proc が消えるまで最大 timeout 待ち、消えたら true を返す。待機中は stopProgressEvery ごとに
// progress(経過時間) を呼ぶ。
func waitExit(proc *os.Process, timeout time.Duration, progress func(time.Duration)) bool {
	start := time.Now()
	deadline := start.Add(timeout)
	nextProgress := start.Add(stopProgressEvery)
	for {
		if proc.Signal(syscall.Signal(0)) != nil {
			return true
		}
		now := time.Now()
		if !now.Before(deadline) {
			return false
		}
		if progress != nil && !now.Before(nextProgress) {
			progress(now.Sub(start))
			nextProgress = nextProgress.Add(stopProgressEvery)
		}
		wait := stopPollEvery
		if rem := deadline.Sub(now); rem < wait {
			wait = rem
		}
		time.Sleep(wait)
	}
}
