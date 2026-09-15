// proc.go — /proc ベースの「この PID は本物の bridge か」判定。
//
// state (bridges.json) の PID は spawn 時の記録で、bridge が外部から kill された後に別プロセスへ
// 再利用されうる。issue #47 では type 未記録の entry が /proc/comm 突合をスキップしたことで、
// 実プロセスの無い handle が 2 日以上 "running" と表示され続けた (幽霊 running)。
// ここでは comm (プロセス名) ではなく argv 全体を読み、「bridge-* バイナリが --participant <handle>
// を独立 argv として持つ」ことまで確認する。bridge package の orphan 検出 (issue #31) と同一基準。
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadCmdline は /proc/<pid>/cmdline を読んでプロセスの argv を返す。
// cmdline は NUL 区切り。読めない (プロセス消滅 / procfs 非対応) 場合は nil を返す。
func ReadCmdline(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil
	}
	parts := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}

// LooksLikeBridgeProcess は argv (あるプロセスの実コマンドライン) が、指定 handle の
// 本物の bridge ワーカー起動であるかを判定する。spawn は常に
//
//	exec.Command(<.../bridge-XXX>, "--participant", <handle>, "--workdir", ...)
//
// の形で bridge を起動するため、本物の bridge は
//   - argv[0] の basename が "bridge-" で始まる
//   - "--participant <handle>" (または旧 "--user <handle>") を「独立した argv 要素」として持つ
//
// という 2 条件を満たす。一方、誤マッチ源は両方を満たさない (issue #31):
//   - "bash -c 'agenthubctl ... --participant <handle> ...'": --participant は -c の
//     文字列の中にあり独立 argv ではない。argv[0]=bash。
//   - "agenthubctl bridge spawn --participant <handle> ...": 独立 argv だが argv[0]=agenthubctl
//     で "bridge-" prefix を持たない。
//
// 旧フラグ --user / 短縮形 -p,-u も受理する (後方互換、旧バイナリ起動の orphan 検出用)。
func LooksLikeBridgeProcess(argv []string, handle string) bool {
	if len(argv) == 0 {
		return false
	}
	if !strings.HasPrefix(filepath.Base(argv[0]), "bridge-") {
		return false
	}
	for i, a := range argv {
		switch a {
		case "--participant", "-p", "--user", "-u":
			if i+1 < len(argv) && argv[i+1] == handle {
				return true
			}
		case "--participant=" + handle, "-p=" + handle, "--user=" + handle, "-u=" + handle:
			return true
		}
	}
	return false
}
