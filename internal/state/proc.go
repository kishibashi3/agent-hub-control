// proc.go — /proc ベースの「この PID は本物の bridge か」判定。
//
// state (bridges.json) の PID は spawn 時の記録で、bridge が外部から kill された後に別プロセスへ
// 再利用されうる。issue #47 では type 未記録の entry が /proc/comm 突合をスキップしたことで、
// 実プロセスの無い handle が 2 日以上 "running" と表示され続けた (幽霊 running)。
// ここでは comm (プロセス名) ではなく argv 全体を読み、「bridge-* バイナリが --participant <handle>
// を独立 argv として持つ」ことまで確認する。bridge package の orphan 検出 (issue #31) と同一基準。
package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ReadCmdline は /proc/<pid>/cmdline を読んでプロセスの argv を返す。
// cmdline は NUL 区切り。読めない (プロセス消滅 / procfs 非対応) 場合は error を返す。
// 読めたが空 (zombie / exec 直後の窓では cmdline が空になる) 場合は空 slice を error 無しで返す —
// 「読めない」と「読めたが argv が無い」を呼び出し側で区別し、stat / exe で切り分けられるようにするため
// (IsBridgeProcess / emptyArgvIsBridge, issue #47 / #65)。
func ReadCmdline(pid int) ([]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimRight(string(data), "\x00")
	if trimmed == "" {
		return []string{}, nil
	}
	return strings.Split(trimmed, "\x00"), nil
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

// ReadExe は /proc/<pid>/exe の readlink 結果 (実行中バイナリの実パス) を返す。
// バイナリが実行中に置き換え・削除されると kernel は " (deleted)" を付けるので取り除く。
// 別 uid のプロセス (EACCES) / 消滅 (ENOENT) / procfs 非対応では error を返す。
func ReadExe(pid int) (string, error) {
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(exe, " (deleted)"), nil
}

// LooksLikeBridgeExe は /proc/<pid>/exe の basename が bridge バイナリの命名不変条件
// ("bridge-" prefix) を満たすかを返す。argv[0] は prctl(PR_SET_NAME) や `exec -a` で偽装できるが、
// exe は kernel が実際に実行しているファイルを指すため偽装できない (issue #50)。
// spawn 時の checkBridgeBinaryName が「与えられたパス」と「symlink 解決後の実体」の両方に同じ
// 不変条件を強制するので、agenthubctl が spawn した ELF bridge は必ず満たす。shebang wrapper は
// exe が interpreter になるため満たさない (既知の制約、PR #66)。
func LooksLikeBridgeExe(exe string) bool {
	return strings.HasPrefix(filepath.Base(exe), "bridge-")
}

// ReadProcState は /proc/<pid>/stat の state 欄 (R/S/D/Z/T/X...) を返す。
// comm は括弧付きで空白や ')' を含みうるので、最後の ')' の後から読む。
func ReadProcState(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", fmt.Errorf("/proc/%d/stat: no ')' in %q", pid, s)
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) == 0 {
		return "", fmt.Errorf("/proc/%d/stat: no state field after comm in %q", pid, s)
	}
	return fields[0], nil
}

// emptyArgvIsBridge は cmdline が空 (argv 無し) の生きた PID を bridge とみなすかを決める (issue #65)。
//
// cmdline が空に見えるプロセスは 3 種類ある:
//   - zombie: mm が解放済み。stat の state は 'Z' (reap 直前は 'X')。→ false (issue #47 の幽霊 running 防止)
//   - exec 直後の窓: execve の中で新 mm に切り替わってから (exec_mmap) ELF loader が
//     arg_start/arg_end を設定するまでの ms 未満の区間。spawn 側の Start() は close-on-exec で
//     exec 成功を検知して戻るため、この窓は state 保存後に status / reconcile から観測されうる。
//     kernel は exe_file を exec_mmap より前に新バイナリへ切り替える (fs/exec.c begin_new_exec) ので、
//     この窓でも /proc/<pid>/exe は既に bridge バイナリを指す。→ exe が bridge-* なら true
//   - kernel thread (kworker 等): mm が無く exe の readlink は ENOENT。PID 再利用先がこれだった場合。→ false
//
// exe が ENOENT 以外で読めない (別 uid の EACCES 等) ときは検証不能なので alive 寄り (true) に倒す —
// 誤って false に倒すと稼働中 bridge が dead 扱いになり watchdog が重複 spawn するため。
// この経路では handle の突合ができない (argv が無い) が、窓は ms 未満かつ spawn 直後の自 PID に
// 限られるので、別 handle の bridge に PID が再利用される確率は無視できる。
func emptyArgvIsBridge(pid int) bool {
	st, err := ReadProcState(pid)
	if err != nil || st == "Z" || st == "X" {
		return false
	}
	exe, err := ReadExe(pid)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	return LooksLikeBridgeExe(exe)
}

// IsBridgeProcess は PID が指定 handle の本物の bridge プロセスかを /proc で判定する。
//
//  1. argv (/proc/<pid>/cmdline) が LooksLikeBridgeProcess を満たすこと
//  2. exe (/proc/<pid>/exe) が読めるなら、その basename も "bridge-" prefix を持つこと
//
// argv が空のときは zombie か exec 直後の窓かを stat / exe で切り分ける (emptyArgvIsBridge, issue #65)。
// exe が読めない (別 uid の bridge を root 以外から見た場合、非 Linux 等) ときは argv 判定のみに
// フォールバックする — 誤って false に倒すと稼働中 bridge が dead 扱いになり watchdog が
// 重複 spawn するため。cmdline 自体が読めないときは error を返し、呼び出し側が
// 従来のフォールバック (comm 突合など) を選べるようにする。
func IsBridgeProcess(pid int, handle string) (bool, error) {
	argv, err := ReadCmdline(pid)
	if err != nil {
		return false, err
	}
	if len(argv) == 0 {
		return emptyArgvIsBridge(pid), nil
	}
	if !LooksLikeBridgeProcess(argv, handle) {
		return false, nil
	}
	if exe, err := ReadExe(pid); err == nil && !LooksLikeBridgeExe(exe) {
		return false, nil
	}
	return true, nil
}
