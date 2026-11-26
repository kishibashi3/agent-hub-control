// ensure_config.go — 稼働中 bridge が必ず bridge config を持つようにする (issue #90)
//
// fleet (systemd timer / launchd) は `bridge start --all` を回すだけで、`start --all` は
// bridge config (`$XDG_CONFIG_HOME/agenthubctl/bridges/<handle>.json`) にある handle しか起こさない。
// spawn / sync が state (bridges.json) だけを書いて config を書かないと、その bridge は
// 「走っているのに宣言的な記録が無い」状態になり、死んだら fleet が何もできない
// (2026-11-09〜11 に @ntv-infra が約 19 時間放置された)。
package bridge

import (
	"fmt"
	"os"
	"strings"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
)

// ensureConfig は handle の bridge config が無ければ want の内容で作成する。
// 既存 config は一切書き換えない (operator が整えた config を spawn のフラグや cmdline 採取値で
// 上書きしない)。作成したら true を返す。
//
// Workdir が空の config は `start` が cwd で起動してしまうので作らずエラーにする。
func ensureConfig(want bridgecfg.BridgeConfig) (bool, error) {
	existing, err := bridgecfg.Load(want.Handle)
	if err != nil {
		return false, err
	}
	if existing != nil {
		return false, nil
	}
	if want.Workdir == "" {
		return false, fmt.Errorf("workdir is unknown; run `agenthubctl bridge config set %s --workdir <path>`", want.Handle)
	}
	if want.BridgeType == "" {
		// 既存の運用 config と同じく type は明示して保存する (default が将来変わっても起動 type が変わらない)。
		want.BridgeType = defaultBridgeType
	}
	if err := bridgecfg.Save(&want); err != nil {
		return false, err
	}
	return true, nil
}

// cmdlineDisplayName は稼働中プロセスの argv から -display-name の値を読む (sync で config を
// 起こすときに display_name を落とさないため)。読めなければ空。
func cmdlineDisplayName(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	return parseDisplayName(strings.Split(string(b), "\x00"))
}

// parseDisplayName は argv から -display-name / --display-name の値を返す。
func parseDisplayName(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-display-name" || args[i] == "--display-name" {
			return args[i+1]
		}
	}
	return ""
}

// configuredHandles は config が保存されている handle の集合を返す。
func configuredHandles() (map[string]bool, error) {
	cfgs, err := bridgecfg.ListAll()
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(cfgs))
	for _, c := range cfgs {
		m[c.Handle] = true
	}
	return m, nil
}
