# agenthubctl

agent-hub の bridge worker lifecycle を管理する CLI。spawn / stop / restart / list /
status / sync / prune / logs / config と、hub への send / inbox / participants を提供する。

## Install

```sh
make install
```

`make install` は **`~/.local/bin`**（`INSTALL_DIR` で上書き可）にバイナリを配置し、
その場所が `PATH` 上にあるかを検査する。`PATH` に無ければ警告と追加コマンドを表示する。
`GOBIN` / `GOPATH/bin` や mise の Go install dir は Go の設定・版で場所が変わるため入れ先にしない（issue #94）:

```
installed agenthubctl -> /home/you/.local/bin/agenthubctl
WARNING: /home/you/.local/bin is NOT on PATH.
         Add it so 'agenthubctl' resolves uniquely:
         export PATH="/home/you/.local/bin:$PATH"
```

さらに、shell が最初に選ぶ `agenthubctl`（`command -v`）が入れ先の copy かも検査する。
`PATH` の前のほうに別の copy（例: mise の Go install dir に残った古い binary）があると警告する（issue #99）。
削除や `PATH` の並べ替えは自動では行わないので、表示された copy を消すか、入れ先を `PATH` の前に置く:

```
installed agenthubctl -> /home/you/.local/bin/agenthubctl
WARNING: 'agenthubctl' resolves to /home/you/old/agenthubctl, not /home/you/.local/bin/agenthubctl.
         Another copy comes earlier on PATH. Remove it or put /home/you/.local/bin first.
```

`PATH` に追加したら、**正規の `agenthubctl` が一意に解決される**ことを確認する:

```sh
which -a agenthubctl       # → 表示される行がすべて ~/.local/bin/agenthubctl であること
agenthubctl version
```

`PATH` に `~/.local/bin` が重複して入っている環境では、`which -a` は同じ path を複数行出す。
すべて `~/.local/bin/agenthubctl` なら問題ない。別の path の行があれば、それが古い copy。

`INSTALL_DIR` は絶対 path で渡す（相対 path や、引用符で囲んで展開されなかった `~` はエラーで止まる。引用しない `INSTALL_DIR=~/x` は shell が `~` を展開するので絶対 path として通る）。
`GOOS` / `GOARCH` を設定した cross-compile は `go install` が拒否するので、`make build` を使う。

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
| `bridge spawn [<handle>] [--workdir …] [--type …] [--tenant …] [--model …] [--bin …]` | bridge 起動（`--bin` は下の「bridge binary の選び方」。`--model` は bridge-claude2 の `-model` に渡る。未指定なら agenthubctl は渡さず、bridge 側が `AGENT_HUB_MODEL` env か内蔵 default で解決。`--model ""` で保存値を上書きして渡さない） |
| `bridge stop <handle> [--timeout …]` | bridge 停止（実プロセスを発見して停止）。SIGTERM 後、bridge の graceful drain（`/compact` と未処理の便）が終わって exit するのを待つ。既定の待ち時間は bridge の drain 上限 + 1m（subprocess timeout 30m なら 37m）で、超えたときだけ SIGKILL。`--timeout 5s` などで短くできる（issue #89）。config があれば `stopped_at` を記録し、fleet（`start --all`）はその bridge を起こさなくなる。process が既に無くても記録する（issue #98）。config が壊れている等で記録できなくても process は止め、記録の失敗は非 0 終了で返す（この場合 fleet は次回また起こす） |
| `bridge restart <handle> \| --all [--stop-timeout …]` | 再起動（display_name / model / bin は bridge config を再読、無ければ前回値。bin は config で付けた固定なら `config set --bin ""` で外れる）。stop 側は `bridge stop` と同じく graceful drain が終わるまで待ってから再 spawn する。既定の待ち時間は bridge の drain 上限 + 1m（`--stop-timeout` で変更、超えたら SIGKILL して再 spawn。SIGKILL 後 5 秒たってもプロセスが残っている場合は再 spawn せずエラーで終了する）。drain を待っている間も state のエントリは残すので、待機中に Ctrl-C・SSH 切断・SIGKILL などで restart が終わっても bridge は state から消えない。旧プロセスが終わった後、config のある bridge は fleet の `bridge start --all` が再 spawn し、config の無い bridge は `bridge restart <handle>` をもう一度実行すれば再 spawn できる。旧プロセスが終わってから restart が再 spawn に移るまでの間に fleet などが先に再 spawn した場合、restart は二重に起動せずエラーで終了する（bridge はそちらが起動したもので動いている。`bridge list` で確認）（issue #97）。単位に注意: `--stop-timeout` は duration（例 `5m`）、`--timeout` は再 spawn 後の ready 待ちで秒（例 `40`）。`--all` は 1 体ずつ順に回す（issue #92） |
| `bridge start <handle> \| --all` | 起動（config から）。`start <handle>` / `spawn` は config の `stopped_at` を消す（明示的に起こした = 動かす）。「already running」で弾かれた場合は消さない。`--all` は `stopped_at` のある bridge を `skipping @x (stopped since …)` で飛ばす（issue #98） |
| `bridge list` | 一覧（untracked 併記、MODEL 列 = agenthubctl が渡した model。未指定は `(not passed)`） |
| `bridge status [handle]` | 状態表示（`status <handle>` は `model: <id> (source: flag\|config\|state\|cmdline)`、`bin: <name> (source: flag\|config\|state)`、固定なしなら出所 env / default を表示（issue #88）） |
| `bridge sync [--dry-run]` | state と実プロセスを整合 |
| `bridge prune [--dry-run]` | dead エントリ削除 |
| `bridge logs [-f] <handle>` | ログ表示（agenthubctl が接続した stdout/stderr。既定 `$AGENT_HUB_HOME/logs/bridge-<handle>.out.log`、`AGENT_HUB_BRIDGE_LOG_DIR` で上書き可。旧 `/tmp/bridge-<handle>.log` は spawn / restart 時に新パスへの symlink に置き換わり移行期間中は参照可（旧バイナリが残した自己所有ログは `.pre-migration` に退避）、将来の minor で削除） |
| `bridge config set/get/list` | spawn 引数の保存（`--workdir` / `--tenant` / `--type` / `--display-name` / `--model` / `--bin`）。`list` の STATE 列 / `get` の `state` は `run`（fleet が起こす）か `stopped since <時刻>`（`bridge stop` で止めてある） |
| `fleet install [--system\|--user] [--dry-run]` | 起動時＋定期で `bridge start --all` する boot-start + watchdog を導入 |
| `fleet uninstall` | boot-start + watchdog を撤去（env ファイルは残す） |
| `fleet status` | 導入状態・timer の有効/稼働・次回発火を表示 |
| `send <@handle> <message>` | DM 送信 |
| `inbox [--mark-read]` | 未読取得 |
| `participants [--online-only]` | 参加者一覧 |
| `version` | version / commit / build date |

## bridge binary の選び方（issue #88）

bridge の binary は `$BIN`（`$AGENT_HUB_HOME/bin`、未設定なら `~/.agent-hub/bin`）の中から選ぶ。
**PATH は見ない**（開発用 build で黙って起動しないため、issue #74）。解決順:

| 順 | 指定 | 出所 (`status` の `bin:`) |
|---|---|---|
| 1 | `bridge spawn --bin <name>`（その spawn だけ。state に記録） | `flag` |
| 2 | `bridge config set <handle> --bin <name>`（永続。fleet の `start --all` / watchdog も読む） | `config` |
| 3 | state に記録した前回の bin（restart / watchdog のみ。config の bin で固定した値は、config の bin を空にすると引き継がない） | `state` |
| 4 | `AGENT_HUB_{TYPE}_BIN`（**deprecated**、移行期間のみ。使うと warning） | `env` |
| 5 | `$BIN/<type>`（既定。bridges 側の `make deploy --activate` が `<type> -> <type>@<revision>` の symlink を張る） | `default` |

どれも無ければ spawn はエラーになり、試した場所と直し方を出す。

`--bin` の値は **`$BIN` の中のファイル名だけ**（例: `bridge-claude2@6019e3d80be5`、`bridge-claude2-344` のように
`<type>@<revision>` に揃っていない名前も可）。`/` を含む値（フルパス・`..`）はエラー。`--bin ""` で固定を外して既定に戻す。

```sh
# 1 体だけ別 build で動かす（watchdog が起こし直しても同じ build のまま）
agenthubctl bridge config set chaos-research --bin bridge-claude2@6019e3d80be5
agenthubctl bridge restart chaos-research
agenthubctl bridge status chaos-research    # bin: で確認
# 戻す
agenthubctl bridge config set chaos-research --bin ""
agenthubctl bridge restart chaos-research
```

1 回だけ試すなら `bridge spawn --bin` でもよいが、**1 体を別 build で動かし続けるなら config に書く**
（restart / watchdog は `spawn --bin` の値を state から引き継ぐが、fleet の `start --all` は引き継がず
config の bin か既定の build で起こす。config が正本）。

`bridge watchdog` は respawn の前に死んだエントリを state から消す。固定した名前が `$BIN` から消えていると
respawn はエラーになり、次の `start --all` は config の bin で起こす（`spawn --bin` だけの固定はここで外れ、
既定の build で起動する）。固定した build を `$BIN` から消す前に `config set --bin ""` で外しておく。

どの経路の値も、symlink を辿った先の実体に対して検査する: 絶対パス・通常ファイル・実行権限・`~` / `$` を
含まない・basename が `bridge-` で始まる（issue #82 / #50）。

移行: `AGENT_HUB_{TYPE}_BIN` が設定されている間は env が既定の symlink より優先される（今までと同じ binary で
動く）。env の行を消すと既定の `$BIN/<type>` に切り替わる。env の読み取り自体の削除は別 issue。

## fleet — durable boot + watchdog

host のスリープ → WSL2 VM 死亡 → bridge 全滅、のような全滅から fleet を自動復帰させる
boot-start + watchdog。`bridge config` に登録した desired-state（`bridge config list`）を
正本に、**起動時（30s 後）＋ 3 分ごと**に冪等な `bridge start --all` を流す。すでに動いて
いる bridge は skip され、落ちているものだけが復活する。`bridge stop` で止めた bridge は config に
`stopped_at` が残るので起こさない（クラッシュで死んだものには付かないので起こす。issue #98）。

current user / HOME / `agenthubctl` の実体 path は install 時に解決されるので、生成される
unit に **machine-specific なハードコード path は一切無い**。秘密は unit ではなく
EnvironmentFile（既定 `~/.agent-hub/fleet.env`, mode 0600）側に置く。

### 前提（desired-state を先に登録）

```sh
agenthubctl bridge config set <handle> -w <workdir> [--tenant <t>] [--model <id>]   # fleet メンバーごと
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
make install                                   # 最新 agenthubctl を ~/.local/bin に配置
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
`claude` CLI を含む `PATH`（nvm / mise の bin）を忘れると spawn が fail-fast する。bridge binary の場所は
env ではなく `$BIN` で決まる（上の「bridge binary の選び方」。`AGENT_HUB_{TYPE}_BIN` は deprecated）。反映は次の watchdog tick（既定 3 分）。

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
| `AGENT_HUB_{TYPE}_BIN` | | **deprecated**（issue #88）。bridge type ごとのバイナリの絶対パス（例: `AGENT_HUB_BRIDGE_CLAUDE2_BIN`）。設定されていれば既定の `$BIN/<type>` より優先し、warning を出す。`~` / `$` を含む値・相対パス・実在しないパスはエラー |
| `AGENT_HUB_HOME` | | state / logs / bin のルート（既定 `~/.agent-hub`。bridge binary は `$AGENT_HUB_HOME/bin`） |
</content>
