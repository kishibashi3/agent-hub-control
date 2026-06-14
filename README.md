# agenthubctl

agent-hub の bridge worker lifecycle を管理する CLI。spawn / stop / restart / list /
status / sync / prune / logs / config と、hub への send / inbox / participants を提供する。

## Install

```sh
make install
```

`make install` は `go install` で **GOBIN**（未設定なら `GOPATH/bin`）にバイナリを配置し、
その場所が `PATH` 上にあるかを検査する。`PATH` に無ければ警告と追加コマンドを表示する:

```
installed agenthubctl -> /home/you/go/bin/agenthubctl
WARNING: /home/you/go/bin is NOT on PATH.
         Add it so 'agenthubctl' resolves uniquely:
         export PATH="/home/you/go/bin:$PATH"
```

`PATH` に追加したら、**正規の `agenthubctl` が一意に解決される**ことを確認する:

```sh
command -v agenthubctl     # → 1 つだけ表示されること
agenthubctl version
```

> **古い binary を踏まないために**: リポジトリ root の `./agenthubctl`（`make build` の生成物）や
> 他リポジトリの別系統 binary（例: `agent-hub-bridges/bridge-tmux/agenthubctl`）を直叩きせず、
> 必ず `make install` した PATH 上の 1 本を使う。どの binary を実行しているか怪しいときは
> `agenthubctl version` で commit / build date を確認する。

## version

```sh
agenthubctl version       # または agenthubctl --version
```

```
agenthubctl dev
  commit: 5a9821429884+dirty
  built:  2026-06-11T15:19:59Z
  go:     go1.22.4
```

commit / build date は go の VCS stamping（`debug.ReadBuildInfo`）から取得するため、
ビルドした git tree の状態がそのまま反映される。`+dirty` は未コミット変更ありを示す。
**古い binary は commit / built が古いので即座に見分けられる。**

## state と実プロセスの整合

`bridges.json`（state）は spawn/stop で更新されるが、外部で起動・kill された bridge とは
ズレうる。**`list` / `stop` は実プロセスを正本にする**:

- `bridge list` — state のエントリに加え、state に無い稼働中 bridge を `untracked` として併記し、
  dead エントリ・untracked プロセスを検出したら `sync` / `prune` をサジェストする。
- `bridge stop <handle>` — state に無くても / state の PID が死んでいても、実際に動いている
  bridge プロセスを発見して停止する（reconcile してから stop）。
- `bridge sync` — dead エントリの削除 + untracked プロセスの adopt を一括で行う。
- `bridge prune` — dead エントリのみ削除。

ゴールは「**operator が `pgrep` / `kill` など生プロセスに直接触れずに済む**」こと（issue #38）。

## 主なコマンド

| command | 用途 |
|---|---|
| `bridge spawn [<handle>] [--workdir …] [--type …] [--tenant …]` | bridge 起動 |
| `bridge stop <handle>` | bridge 停止（実プロセスを発見して停止） |
| `bridge restart <handle> \| --all` | 再起動 |
| `bridge start <handle> \| --all` | 起動（config から） |
| `bridge list` | 一覧（untracked 併記） |
| `bridge status [handle]` | 状態表示 |
| `bridge sync [--dry-run]` | state と実プロセスを整合 |
| `bridge prune [--dry-run]` | dead エントリ削除 |
| `bridge logs [-f] <handle>` | ログ表示 |
| `bridge config set/get/list` | spawn 引数の保存 |
| `send <@handle> <message>` | DM 送信 |
| `inbox [--mark-read]` | 未読取得 |
| `participants [--online-only]` | 参加者一覧 |
| `version` | version / commit / build date |

## 環境変数

| 変数 | 必須 | 用途 |
|---|---|---|
| `AGENT_HUB_URL` | ✓ | agent-hub MCP エンドポイント |
| `GITHUB_PAT` | ✓ | GitHub PAT（pat モード） |
| `AGENT_HUB_TENANT` | | テナント ID |
| `AGENT_HUB_USER` | | handle override |
| `AGENT_HUB_{TYPE}_BIN` | | bridge type ごとのバイナリパス（例: `AGENT_HUB_BRIDGE_CLAUDE2_BIN`） |
| `AGENT_HUB_HOME` | | state ディレクトリ（既定 `~/.agent-hub`） |
</content>
