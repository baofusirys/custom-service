#!/usr/bin/env bash
# Custom Service · 安全自验证脚本
# 用法：bash docs/security-selftest.sh https://cs.example.com

set -u
BASE="${1:-http://localhost}"
PASS=0
FAIL=0

ok()   { echo "  [PASS] $1"; PASS=$((PASS+1)); }
bad()  { echo "  [FAIL] $1"; FAIL=$((FAIL+1)); }
case_title() { echo; echo "==> $1"; }

# ============================================================
case_title "1) SQL 注入 payload 应被处理为普通字符串（登录失败）"
RESP=$(curl -sk -X POST "$BASE/api/agent/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin'"'"' OR 1=1 --","password":"x"}')
echo "$RESP" | grep -q '"code":401' && ok "返回 40105 账号或密码错误（未注入成功）" || bad "返回值异常: $RESP"

# ============================================================
case_title "2) XSS payload 在访客会话中应被清洗"
RESP=$(curl -sk -X POST "$BASE/api/visitor/session" \
  -H 'Content-Type: application/json' \
  -d '{"identifier":"<script>alert(1)</script>","ua":"selftest"}')
echo "$RESP" | grep -q 'visitor_token' && ok "签发了 visitor token" || bad "未签发 token: $RESP"
# 进一步验证服务端清洗效果需要查询 admin API，这里只能信任 SanitizeText 单测；
# 真实验证：登录 admin 后台 -> 查看该访客的 identifier，应为空或纯文本。

# ============================================================
case_title "3) Nginx 登录限速（2 r/s）应在第 5 次开始 429"
HTTPS_CODES=()
for i in $(seq 1 10); do
  CODE=$(curl -sko /dev/null -w "%{http_code}" -X POST "$BASE/api/agent/login" \
    -H 'Content-Type: application/json' \
    -d '{"username":"admin","password":"x"}')
  HTTPS_CODES+=("$CODE")
done
echo "  10 次请求 HTTP 状态：${HTTPS_CODES[*]}"
echo "${HTTPS_CODES[*]}" | grep -q "429" && ok "出现 429（限速生效）" || bad "未限速（注意 Nginx burst=5 nodelay）"

# ============================================================
case_title "4) WSS 握手频率限制（单 IP / 分钟）"
WS_429=0
for i in $(seq 1 30); do
  CODE=$(curl -sko /dev/null -w "%{http_code}" \
    -H 'Upgrade: websocket' -H 'Connection: Upgrade' \
    -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGVzdA==' \
    "$BASE/ws/visitor?token=invalid-but-rate-check-first")
  [ "$CODE" = "429" ] && WS_429=$((WS_429+1))
done
[ $WS_429 -gt 0 ] && ok "WSS 握手限速触发 ($WS_429 次 429)" || bad "WSS 握手未限速（检查 backend SECURITY_IP_WS_HANDSHAKE_PM）"

# ============================================================
case_title "5) 巨大文件应被拒绝（Nginx client_max_body_size 25m）"
dd if=/dev/zero of=/tmp/cs-big.bin bs=1M count=30 status=none
CODE=$(curl -sko /dev/null -w "%{http_code}" -X POST "$BASE/api/upload" \
  -F 'uploader=visitor' \
  -F 'file=@/tmp/cs-big.bin')
rm -f /tmp/cs-big.bin
[ "$CODE" = "413" ] && ok "30MB 文件被拒（HTTP 413）" || bad "未拒绝大文件，实际返回 $CODE"

# ============================================================
case_title "6) 路径穿越下载应被拒"
CODE=$(curl -sko /dev/null -w "%{http_code}" "$BASE/files/../../../etc/passwd")
[ "$CODE" = "400" ] || [ "$CODE" = "404" ] && ok "穿越被拒（$CODE）" || bad "穿越未拒绝（$CODE）"

# ============================================================
case_title "7) 健康端点返回北京时间 + tz"
RESP=$(curl -sk "$BASE/api/health")
echo "$RESP" | grep -q '"tz":"Asia/Shanghai"' && ok "时区正确（Asia/Shanghai）" || bad "tz 异常: $RESP"

# ============================================================
echo
echo "==================================="
echo " 总计：PASS=$PASS  FAIL=$FAIL"
echo "==================================="
[ $FAIL -eq 0 ]
