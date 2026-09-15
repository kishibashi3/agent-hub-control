// logpath.go — agenthubctl が bridge の stdout/stderr を接続するログファイルの置き場所 (issue #54)。
//
// 従来は /tmp/bridge-<handle>.log 固定だった。/tmp は (a) テストと実運用が同じパスを共有して
// 汚染し合う、(b) 複数の AGENT_HUB_HOME (= 複数 fleet) が同一ホストで衝突する、(c) 予測可能な
// world-writable ディレクトリ上のパスで symlink 攻撃面になる、という問題があったため、state
// (bridges.json) と同じく AGENT_HUB_HOME 配下に置く。default は bridge-claude2 自身の slog 正本
// (~/.agent-hub/logs/bridge-<handle>.log) と同じディレクトリで、ファイル名を .out.log にして区別する
// (fleet の fleet.out.log / fleet.err.log と同じ命名)。
package state

import (
	"fmt"
	"os"
	"path/filepath"
)

// BridgeLogDirEnv は agenthubctl 側のログディレクトリを上書きする環境変数。
const BridgeLogDirEnv = "AGENT_HUB_BRIDGE_LOG_DIR"

// homeDir は AGENT_HUB_HOME (未設定なら ~/.agent-hub) を返す。state / logs の共通ルート。
func homeDir() (string, error) {
	if base := os.Getenv("AGENT_HUB_HOME"); base != "" {
		return base, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(dir, ".agent-hub"), nil
}

// BridgeLogDir は agenthubctl が bridge の stdout/stderr を書くディレクトリを返す。
// 優先順位: AGENT_HUB_BRIDGE_LOG_DIR > $AGENT_HUB_HOME/logs > ~/.agent-hub/logs
func BridgeLogDir() (string, error) {
	if d := os.Getenv(BridgeLogDirEnv); d != "" {
		return d, nil
	}
	base, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "logs"), nil
}

// BridgeLogPath は handle の stdout/stderr ログ (bridge-<handle>.out.log) のパスを返す。
// spawn / sync / test はすべてこの関数を経由し、パス文字列を各所で組み立てない。
func BridgeLogPath(handle string) (string, error) {
	dir, err := BridgeLogDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bridge-"+handle+".out.log"), nil
}

// LegacyBridgeLogPath は旧固定パス (<dir>/bridge-<handle>.log) を返す。dir は通常 "/tmp"
// (bridge package の legacyLogDir)。段階 deprecation の互換用で、新規コードは BridgeLogPath を使うこと。
func LegacyBridgeLogPath(dir, handle string) string {
	return filepath.Join(dir, "bridge-"+handle+".log")
}
