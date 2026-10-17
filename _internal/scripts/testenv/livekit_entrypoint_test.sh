#!/bin/sh
# cs-livekit 入口脚本本地校验测试（不运行 Docker、不连接任何服务器）。
# 用法（仓库根目录）：sh _internal/scripts/testenv/livekit_entrypoint_test.sh
# 做法：复制 livekit/entrypoint.sh 到临时目录，把 /livekit-server 与配置路径替换为本地桩，
# 逐项验证「正常 / 拒绝 / 绕过」场景；测试密钥每次随机生成，绝不输出密钥值。
set -u

ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
SRC="$ROOT/livekit/entrypoint.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
check() {
  if [ "$1" = "0" ]; then pass=$((pass + 1)); echo "PASS $2"; else fail=$((fail + 1)); echo "FAIL $2"; fi
}

# 桩：模拟 livekit-server，校验入口脚本交给它的环境变量与参数（只输出校验结论，不输出值）
cat > "$WORK/livekit-server" <<'EOF'
#!/bin/sh
ok=1
[ "$LIVEKIT_KEYS" = "\"$EXPECT_KEY\": \"$EXPECT_SECRET\"" ] || { echo "STUB keys_mismatch"; ok=0; }
[ "$LIVEKIT_WEBHOOK_API_KEY" = "$EXPECT_KEY" ] || { echo "STUB webhook_key_mismatch"; ok=0; }
[ -z "${LIVEKIT_API_SECRET+x}" ] || { echo "STUB raw_secret_still_exported"; ok=0; }
[ -z "${LIVEKIT_CONFIG+x}" ] || { echo "STUB livekit_config_not_cleared"; ok=0; }
[ "$NODE_IP" = "$EXPECT_IP" ] || { echo "STUB node_ip_env_mismatch"; ok=0; }
[ "$*" = "--config $EXPECT_CONF --node-ip $EXPECT_IP" ] || { echo "STUB args_mismatch"; ok=0; }
[ "$ok" = 1 ] && echo "STUB_OK"
exit 0
EOF
chmod +x "$WORK/livekit-server"
printf 'port: 7880\n' > "$WORK/livekit.yaml"
sed -e "s#^LIVEKIT_BIN=/livekit-server#LIVEKIT_BIN=$WORK/livekit-server#" \
    -e "s#^CONFIG_FILE=/etc/livekit/livekit.yaml#CONFIG_FILE=$WORK/livekit.yaml#" "$SRC" > "$WORK/entry.sh"
grep -q "^LIVEKIT_BIN=$WORK/livekit-server" "$WORK/entry.sh"; check $? "桩替换成功"

rand_hex() { od -An -tx1 -N "$1" /dev/urandom | tr -d ' \n'; }
KEY="API$(rand_hex 6)"
SECRET=$(rand_hex 32)

# run <期望退出码> <描述> <环境变量赋值...>：在干净环境中执行，输出写入 $WORK/out
run() {
  want=$1; desc=$2; shift 2
  env -i PATH="$PATH" EXPECT_KEY="$KEY" EXPECT_SECRET="$SECRET" EXPECT_IP="${EXPECT_IP_OVERRIDE:-203.0.113.10}" \
    EXPECT_CONF="$WORK/livekit.yaml" "$@" sh "$WORK/entry.sh" > "$WORK/out" 2>&1
  got=$?
  if [ "$got" = "$want" ]; then r=0; else r=1; echo "  期望退出码 $want 实际 $got"; sed 's/^/  | /' "$WORK/out"; fi
  if grep -q -- "$SECRET" "$WORK/out"; then echo "  输出中出现了密钥值"; r=1; fi
  check $r "$desc"
}

# ---------- 正常 ----------
run 0 "正常：合法公网 IP 启动，LIVEKIT_KEYS/webhook key/参数正确，原始密钥变量已清除" \
  LIVEKIT_API_KEY="$KEY" LIVEKIT_API_SECRET="$SECRET" NODE_IP=203.0.113.10 LIVEKIT_CONFIG=evil
grep -q STUB_OK "$WORK/out"; check $? "桩确认环境与参数"
grep -q '"event":"livekit_starting"' "$WORK/out" && grep -q '+08:00' "$WORK/out"; check $? "启动日志为 JSON 且带 +08:00"

EXPECT_IP_OVERRIDE=10.0.0.5 run 0 "私网 NODE_IP 告警但放行" LIVEKIT_API_KEY="$KEY" LIVEKIT_API_SECRET="$SECRET" NODE_IP=10.0.0.5
grep -q '"event":"node_ip_private"' "$WORK/out"; check $? "私网 NODE_IP 输出 WARN"

B64=$(od -An -tx1 -N 32 /dev/urandom | tr -d ' \n' | sed 's/a/+/g; s/b/\//g; s/c/=/g')
SECRET_SAVE=$SECRET; SECRET=$B64
run 0 "base64 字符集密钥可用" LIVEKIT_API_KEY="$KEY" LIVEKIT_API_SECRET="$B64" NODE_IP=203.0.113.10
SECRET=$SECRET_SAVE

# ---------- 拒绝 ----------
run 78 "只设置 LIVEKIT_API_SECRET 拒绝启动" LIVEKIT_API_SECRET="$SECRET" NODE_IP=203.0.113.10
run 78 "只设置 LIVEKIT_API_KEY 拒绝启动" LIVEKIT_API_KEY="$KEY" NODE_IP=203.0.113.10
run 78 "示例 Key devkey 拒绝" LIVEKIT_API_KEY=devkey LIVEKIT_API_SECRET="$SECRET" NODE_IP=203.0.113.10
run 78 "Key 含冒号拒绝（防 LIVEKIT_KEYS 注入）" LIVEKIT_API_KEY="ab:cd" LIVEKIT_API_SECRET="$SECRET" NODE_IP=203.0.113.10
run 78 "Key 过短拒绝" LIVEKIT_API_KEY="ab" LIVEKIT_API_SECRET="$SECRET" NODE_IP=203.0.113.10
run 78 "Secret 少于 32 字符拒绝" LIVEKIT_API_KEY="$KEY" LIVEKIT_API_SECRET="0123456789abcdef" NODE_IP=203.0.113.10
run 78 "Secret 含引号拒绝（防 YAML 注入）" LIVEKIT_API_KEY="$KEY" LIVEKIT_API_SECRET="${SECRET}\"x" NODE_IP=203.0.113.10
run 78 "Secret 含空格冒号拒绝" LIVEKIT_API_KEY="$KEY" LIVEKIT_API_SECRET="${SECRET}: y" NODE_IP=203.0.113.10
run 78 "Secret 占位值拒绝" LIVEKIT_API_KEY="$KEY" LIVEKIT_API_SECRET="please-changeme-0123456789abcdefghijklmn" NODE_IP=203.0.113.10
run 78 "Secret 低熵拒绝" LIVEKIT_API_KEY="$KEY" LIVEKIT_API_SECRET="abababababababababababababababababab" NODE_IP=203.0.113.10
run 78 "NODE_IP 缺失拒绝" LIVEKIT_API_KEY="$KEY" LIVEKIT_API_SECRET="$SECRET"
for bad in "1.2.3" "256.1.1.1" "1.2.3.4.5" "01.2.3.4" "0.0.0.0" "127.0.0.1" "169.254.1.1" "224.0.0.1" "1.2.3.4 " "a.b.c.d" "1..2.3" "::1"; do
  run 78 "NODE_IP 非法拒绝：[$bad]" LIVEKIT_API_KEY="$KEY" LIVEKIT_API_SECRET="$SECRET" NODE_IP="$bad"
done
grep -q '"level":"FATAL"' "$WORK/out"; check $? "拒绝时输出 FATAL 日志"

# ---------- 待机（两个密钥都未设置：大厅关闭）----------
env -i PATH="$PATH" sh "$WORK/entry.sh" > "$WORK/standby" 2>&1 &
spid=$!
sleep 1
kill -0 "$spid" 2>/dev/null; check $? "未配置密钥时待机不退出（不重启刷屏）"
kill -TERM "$spid" 2>/dev/null
wait "$spid"; st=$?
[ "$st" = 0 ]; check $? "待机收到 SIGTERM 正常退出（退出码 $st）"
grep -q '"event":"hall_media_standby"' "$WORK/standby"; check $? "待机输出 WARN 日志"

echo "SUMMARY pass=$pass fail=$fail"
[ "$fail" = 0 ]
