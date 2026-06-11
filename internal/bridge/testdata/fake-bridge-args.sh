#!/bin/sh
# テスト用: 引数を stdout に出力し "registered and listening" をログして待機
# runSpawn が --participant フラグを正しく渡しているかを確認するために使う
echo "args: $*"
echo "registered and listening"
sleep 5
