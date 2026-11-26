// bindir.go — bridge binary ディレクトリ ($AGENT_HUB_HOME/bin, issue #88)
package state

import "path/filepath"

// BinDir は bridge binary を置くディレクトリ ($BIN) を返す: $AGENT_HUB_HOME/bin
// (AGENT_HUB_HOME 未設定なら ~/.agent-hub/bin)。state / logs と同じルートに従うので、テストや
// 別 fleet が本番の binary を使わない。bridges 側の `make deploy --activate` が
// <type> -> <type>@<revision> の symlink をここに張る。
func BinDir() (string, error) {
	base, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "bin"), nil
}
