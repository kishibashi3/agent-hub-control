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
  **running 判定は PID 生存ではなく実プロセス突合**: `/proc/<pid>/cmdline` を読み、その PID が
  `bridge-*` バイナリで `--participant <handle>` を持つことを確認する。kill 後に PID が別プロセス
  へ再利用されても「幽霊 running」にならない（issue #47。旧実装は bridge type 未記録の entry で
  この突合をスキップしていた）。同じ判定を `start --all` / `status` / watchdog も共有する。
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
| `fleet install [--system\|--user] [--dry-run]` | 起動時＋定期で `bridge start --all` する boot-start + watchdog を導入 |
| `fleet uninstall` | boot-start + watchdog を撤去（env ファイルは残す） |
| `fleet status` | 導入状態・timer の有効/稼働・次回発火を表示 |
| `send <@handle> <message>` | DM 送信 |
| `inbox [--mark-read]` | 未読取得 |
| `participants [--online-only]` | 参加者一覧 |
| `version` | version / commit / build date |

## fleet — durable boot + watchdog

host のスリープ → WSL2 VM 死亡 → bridge 全滅、のような全滅から fleet を自動復帰させる
boot-start + watchdog。`bridge config` に登録した desired-state（`bridge config list`）を
正本に、**起動時（30s 後）＋ 3 分ごと**に冪等な `bridge start --all` を流す。すでに動いて
いる bridge は skip され、落ちているものだけが復活する。

current user / HOME / `agenthubctl` の実体 path は install 時に解決されるので、生成される
unit に **machine-specific なハードコード path は一切無い**。秘密は unit ではなく
EnvironmentFile（既定 `~/.agent-hub/fleet.env`, mode 0600）側に置く。

### 前提（desired-state を先に登録）

```sh
agenthubctl bridge config set <handle> -w <workdir> [--tenant <t>]   # fleet メンバーごと
agenthubctl bridge config list                                       # 正本を確認
```

### install

```sh
# 何が書かれるか先に確認（ファイルは書かない）
agenthubctl fleet install --dry-run

# 導入（root で実行 = system-level / 非 root = user-level に自動判定）
agenthubctl fleet install
```

#### sudo 無しでの導入（推奨: user-level timer）

`/etc` に触れない user-level timer なら root は一切不要。boot-start に必要な linger も
自分自身に対しては sudo 無しで有効化できる。

```sh
make install                                   # 最新 agenthubctl を GOBIN に配置
agenthubctl fleet install --user               # ~/.config/systemd/user/ に unit を生成 + enable --now
loginctl enable-linger "$USER"                 # ログイン無しで boot-start
$EDITOR ~/.agent-hub/fleet.env                 # 下記「fleet.env の中身」
journalctl --user -u agent-hub-fleet.service -n 3   # "Finished ..." が出れば導入完了
```

**旧 system-level unit（`source ~/.bashrc` 版）が `/etc/systemd/system` に残っている場合**:
そのままだと install は「二重 watchdog になる」として中断する（issue #42）。root が無く撤去
できないときは `--force` を付けると **警告を出して user-level 導入を続行する**。旧 unit は
root が取れたときに `sudo agenthubctl fleet uninstall --system` で撤去する（`bridge start --all`
は冪等なので二重期間は無駄撃ちになるだけで壊れはしない）。

```sh
agenthubctl fleet install --user --force
```

#### fleet.env の中身 — なぜ `~/.bashrc` を source しないか

Debian 標準の `~/.bashrc` は先頭で `case $- in *i*) ;; *) return;; esac` と非対話 shell を
即 return するため、`ExecStart=/bin/bash -c 'source ~/.bashrc; agenthubctl ...'` では PATH も
`AGENT_HUB_*` も一切載らず `agenthubctl: command not found`（exit 127）で毎回失敗する。
timer 自体は `active (waiting)` に見えるので気づけない（2026-07-29 / 2026-09-13 の 2 度の
全 bridge 復旧不能の原因, issue #47）。

そのため生成 unit は shell を経由せず `EnvironmentFile=~/.agent-hub/fleet.env` から env を
読み、`ExecStart` は agenthubctl の絶対 path を直接叩く。`fleet.env` には **手で spawn した
bridge が持っている env をそのまま**書く。稼働中 bridge から写すのが確実:

```sh
# 稼働中 bridge の PID を bridge list で確認し、bridge 起動に要る変数だけ抜き出す
tr '\0' '\n' < /proc/<pid>/environ \
  | grep -E '^(PATH|HOME|NVM_DIR|GITHUB_PAT|AGENT_HUB_[A-Z_0-9]+|NODE_TLS_REJECT_UNAUTHORIZED)=' \
  | sed -E 's/^([A-Z_0-9]+)=(.*)$/\1="\2"/' >> ~/.agent-hub/fleet.env
```

書式は `KEY="value"` 1 行 1 変数（systemd EnvironmentFile 互換。`export` や `$VAR` 展開は不可）。
`AGENT_HUB_BRIDGE_CLAUDE2_BIN` のような bridge binary の場所、`claude` CLI を含む `PATH`
（nvm / mise の bin）を忘れると spawn が fail-fast する。反映は次の watchdog tick（既定 3 分）。

- **OS 検出**: Linux + systemd → `.service` + `.timer`、macOS → launchd LaunchAgent `.plist`。
- **scope**: root（`sudo`）なら `/etc/systemd/system` の system-level（ログイン不要で boot-start）。
  非 root なら `~/.config/systemd/user` の user-level。`--system` / `--user` で明示指定も可。
- **user-level の boot-start**: ログインせず再起動を跨ぐには linger を有効化する（自分自身への
  enable-linger は sudo 不要）:
  ```sh
  loginctl enable-linger <user>
  ```
- 導入後、`~/.agent-hub/fleet.env` に `GITHUB_PAT` / `AGENT_HUB_URL` 等を記入する（PATH は
  install 時の値が雛形に取り込まれる）。次の watchdog tick で反映される。

調整フラグ: `--binary <path>`（unit に焼く agenthubctl）、`--env-file <path>`、
`--timeout <sec>`（bridge ごとの ready 待ち, 既定 40）、`--watchdog-interval <sec>`（既定 180）。

### uninstall / status

```sh
agenthubctl fleet uninstall    # unit を撤去（秘密を含みうる env ファイルは残す）
agenthubctl fleet status       # 導入状態・is-enabled/is-active・次回発火
```

### 設計メモ

- `Type=oneshot` で良いのは、bridge が `Setsid` で `init.scope` に脱出し（`sysproc_linux.go`）
  service 終了で reap されないため。`KillMode=process` は belt-and-suspenders。
- launchd は `RunAtLoad`（boot/login 起動）＋ `StartInterval`（watchdog）で同じ挙動を作り、
  `KeepAlive` は**使わない**（oneshot を tight loop で再起動してしまうため）。
- macOS 実機は未検証。plist の妥当性は render テスト（XML well-formed + 構造）で CI 保証し、
  Mac 側では `plutil -lint` で確認する。

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
