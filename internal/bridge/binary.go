// binary.go — bridge binary の解決 (issue #88)
//
// 解決順 (PATH は見ない):
//
//	spawn --bin <name>                  (その 1 回の spawn。state に記録)
//	  > bridge config の "bin"          (per-bridge, 永続)
//	  > state に記録した前回の bin      (restart / watchdog のみ。model と同じ引き継ぎ)
//	  > AGENT_HUB_{TYPE}_BIN            (移行期間のみ。使ったら deprecation warning)
//	  > $BIN/<type>                     (既定。bridges の `make deploy --activate` が張る symlink)
//	無ければエラー。
//
// $BIN は $AGENT_HUB_HOME/bin (未設定なら ~/.agent-hub/bin)。--bin / config の bin の値は $BIN の中の
// ファイル名だけで、"/" を含む値 (フルパス・"..") は受け付けない。どの経路の値でも、symlink を辿った先の
// 実体に対して検査 (絶対パス・通常ファイル・実行権限・'~'/'$' を含まない・basename が bridge-*) をかける
// (issue #82 の検査を吸収)。
package bridge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kishibashi3/agent-hub-control/internal/bridgecfg"
	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// binSpec は spawn に渡す binary の固定指定 ($BIN 内のファイル名) とその出所。
// Name が空なら固定しない (env か既定の $BIN/<type> で解決する)。
type binSpec struct {
	Name   string
	Source string
}

const (
	binSourceFlag    = "flag"    // spawn --bin
	binSourceConfig  = "config"  // bridge config の bin
	binSourceState   = "state"   // restart / watchdog が前回 spawn 時の値を引き継いだ
	binSourceEnv     = "env"     // AGENT_HUB_{TYPE}_BIN (deprecated)
	binSourceDefault = "default" // $BIN/<type>
)

// resolvedBinary は resolveBinary の結果。Path は exec に渡すパス (symlink のまま)、Real は symlink を
// 辿った先の実体。Real ではなく Path を exec するのは argv[0] を $BIN/<type> のまま保つため (sync は argv[0]
// の basename を bridge type として拾い直す)。検査から exec までの間に symlink が張り替えられると、ログの
// Real と実際に動く build がずれうる。$BIN に書けるのは同じ uid だけなので許容する (PR #104 review Suggestion 6)。Name は state に記録する固定指定 (flag / config / state のときだけ非空)。
type resolvedBinary struct {
	Path   string
	Real   string
	Name   string
	Source string
}

// binEnvVar は bridgeType に対応する移行期間用の env 名を返す (例: bridge-claude2 → AGENT_HUB_BRIDGE_CLAUDE2_BIN)。
func binEnvVar(bridgeType string) string {
	return "AGENT_HUB_" + strings.ToUpper(strings.ReplaceAll(bridgeType, "-", "_")) + "_BIN"
}

// validateBinName は --bin / config の bin の値 ($BIN の中のファイル名) を検査する。
// "/" を含む値・"." / ".." は $BIN の外 (またはディレクトリ) を指せるので受け付けない。
func validateBinName(name string) error {
	switch {
	case name == "":
		return errors.New("bin must not be empty")
	case strings.Contains(name, "/"):
		return fmt.Errorf("bin %q must be a file name in the bin directory, not a path (no '/')", name)
	case name == "." || name == "..":
		return fmt.Errorf("bin %q is not a file name", name)
	case strings.ContainsAny(name, "~$"):
		return fmt.Errorf("bin %q must not contain '~' or '$'", name)
	}
	return nil
}

// resolveBinary は bridge binary のパスを決める。pin は flag / config / state から来た固定指定
// (Name 空なら固定なし)。
func resolveBinary(bridgeType string, pin binSpec) (resolvedBinary, error) {
	binDir, err := state.BinDir()
	if err != nil {
		return resolvedBinary{}, fmt.Errorf("resolve bin dir: %w", err)
	}

	if pin.Name != "" {
		if err := validateBinName(pin.Name); err != nil {
			return resolvedBinary{}, fmt.Errorf("%s (source: %s)", err, pin.Source)
		}
		p := filepath.Join(binDir, pin.Name)
		real, err := checkBridgeBinary(p)
		if err != nil {
			return resolvedBinary{}, fmt.Errorf("bin %q (source: %s): %w", pin.Name, pin.Source, err)
		}
		return resolvedBinary{Path: p, Real: real, Name: pin.Name, Source: pin.Source}, nil
	}

	envVar := binEnvVar(bridgeType)
	if v := os.Getenv(envVar); v != "" {
		real, err := checkBridgeBinary(v)
		if err != nil {
			return resolvedBinary{}, fmt.Errorf("%s=%q: %w", envVar, v, err)
		}
		fmt.Fprintf(os.Stderr, "warning: %s is deprecated (issue #88); using %s. Put the binary in %s and select it with `bridge config set <handle> --bin <name>`, or remove the env to use %s\n",
			envVar, v, binDir, filepath.Join(binDir, bridgeType))
		return resolvedBinary{Path: v, Real: real, Source: binSourceEnv}, nil
	}

	if err := validateBinName(bridgeType); err != nil {
		return resolvedBinary{}, fmt.Errorf("bridge type %q cannot be used as a file name in %s: %w", bridgeType, binDir, err)
	}
	p := filepath.Join(binDir, bridgeType)
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return resolvedBinary{}, fmt.Errorf("no %s binary: tried %s (not found) and %s (unset); PATH is not searched. Deploy it with `make deploy --activate` in agent-hub-bridges (creates %s -> %s@<revision>), or pin a file in %s with `agenthubctl bridge config set <handle> --bin <name>`",
			bridgeType, p, envVar, p, bridgeType, binDir)
	}
	real, err := checkBridgeBinary(p)
	if err != nil {
		return resolvedBinary{}, fmt.Errorf("default %s: %w", p, err)
	}
	return resolvedBinary{Path: p, Real: real, Source: binSourceDefault}, nil
}

// checkBridgeBinary は exec するパス p と、symlink を辿った先の実体の両方を検査し、実体のパスを返す。
//   - p も実体も絶対パスで、'~' / '$' を含まない (systemd EnvironmentFile は展開しないので未展開の値が
//     届きうる。相対パスは cwd 次第で systemd と shell で別物を指す。issue #82)
//   - 実体が通常ファイルで実行権限がある (ディレクトリ・非実行ファイルは Start() まで失敗が遅れる)
//   - p と実体の basename が bridge-* (IsRunning / pgrep の判定が依存、issue #50)
//
// 実体まで見るのは、/proc/<pid>/exe が指すのは実体であり、symlink の名前だけでは期待と違う binary が
// 動いていることを検知できないため (2026-10-18 operator 付記)。
func checkBridgeBinary(p string) (string, error) {
	if strings.ContainsAny(p, "~$") {
		return "", errors.New("contains an unexpanded '~' or '$' (systemd EnvironmentFile does not expand them); use an absolute path")
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("is a relative path (resolved against the current directory, which differs between systemd and shell); use an absolute path")
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("not found: %w", err)
	}
	if strings.ContainsAny(real, "~$") {
		return "", fmt.Errorf("symlink target %q contains '~' or '$'", real)
	}
	if !filepath.IsAbs(real) {
		// EvalSymlinks は絶対パスを渡せば絶対パスを返すので通常は到達しない (保険)。
		return "", fmt.Errorf("symlink target %q is not an absolute path", real)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("symlink target %q: %w", real, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file (mode %s)", real, fi.Mode().Type())
	}
	if fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%q is not executable (mode %s)", real, fi.Mode().Perm())
	}
	if !state.LooksLikeBridgeExe(p) {
		return "", errors.New(bridgeBinaryNameHint)
	}
	if !state.LooksLikeBridgeExe(real) {
		return "", fmt.Errorf("symlink target %q: %s", real, bridgeBinaryNameHint)
	}
	return real, nil
}

// bridgeBinaryNameHint は bridge バイナリの命名不変条件を破ったときのエラー文。
// IsRunning / pgrepHandle は argv[0] と /proc/<pid>/exe の basename が "bridge-" で始まることで
// 本物の bridge を識別する (issue #47 / #50)。この不変条件を満たさないバイナリを spawn すると、
// 起動直後から恒久的に dead 判定され fleet watchdog が毎 tick 重複 spawn するため、spawn 時点で拒否する。
// exe は symlink 解決後の実ファイルを指すため、symlink や wrapper の名前を変えても通らない —
// 実体 (ELF) 自体を "bridge-*" に rename する必要がある。
const bridgeBinaryNameHint = "bridge binary basename must start with \"bridge-\" (process identification relies on it; rename the real binary itself — a symlink or wrapper named bridge-* is not enough because /proc/<pid>/exe resolves to the target)"

// pinFromConfigOrState は restart / watchdog の固定指定: bridge config の bin > state に記録した前回の bin。
// cfg は読めた bridge config (無い・読めないなら nil)。config が読めない場合は state にフォールバックする
// (model の restart と同じく config 読み取り失敗で止めない)。
//
// state の前回値が config 由来 (BinSource == "config") で、いまの config の bin が空なら固定しない。
// `config set --bin ""` → restart / watchdog で固定が外れるようにするため (PR #104 review 指摘 1)。
// spawn --bin (flag) 由来の値は config が空でも引き継ぐ (spawn が作る config には bin を書かないため)。
func pinFromConfigOrState(entry *state.Entry, cfg *bridgecfg.BridgeConfig) binSpec {
	if cfg != nil && cfg.Bin != "" {
		return binSpec{Name: cfg.Bin, Source: binSourceConfig}
	}
	if entry == nil || entry.Bin == "" || (cfg != nil && entry.BinSource == binSourceConfig) {
		return binSpec{}
	}
	return binSpec{Name: entry.Bin, Source: binSourceState}
}
