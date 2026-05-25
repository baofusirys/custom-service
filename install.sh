#!/usr/bin/env bash
# ============================================================
# custom_service 一键安装脚本
#
# 用法（在你的服务器上跑一行）：
#   bash <(curl -fsSL https://raw.githubusercontent.com/baofusirys/custom-service/main/install.sh)
#
# 这脚本会：
#   1) 检查 + 装 Docker（Linux）
#   2) 创建 /srv/cs-data/{logs,uploads,ssl} 数据目录
#   3) 下载 docker-compose.production.yml + .env.example
#   4) 自动生成 6 个强密码（用 openssl rand）
#   5) 提示你填 PUBLIC_DOMAIN / ACME_EMAIL / TURN_EXTERNAL_IP 这 3 个必填项
#   6) docker compose pull && up -d
# ============================================================
set -euo pipefail

CYAN='\033[0;36m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
say()  { echo -e "${CYAN}[install] $*${NC}"; }
ok()   { echo -e "${GREEN}[ ✓ ] $*${NC}"; }
warn() { echo -e "${YELLOW}[ ! ] $*${NC}"; }
die()  { echo -e "${RED}[ x ] $*${NC}" >&2; exit 1; }

REPO_RAW="https://raw.githubusercontent.com/baofusirys/custom-service/main"
INSTALL_DIR="${INSTALL_DIR:-/opt/custom-service}"
DATA_DIR="${HOST_DATA_DIR:-/srv/cs-data}"

# ---- 1. 前置检查 ----
say "1/6 检查运行环境"
[[ $EUID -eq 0 ]] || die "请用 root 跑（sudo bash install.sh）"
command -v curl >/dev/null || die "缺 curl，请先 apt install curl / yum install curl"

# ---- 2. 装 Docker ----
say "2/6 检查 Docker"
if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
  ok "Docker + Compose 已就绪：$(docker --version)"
else
  warn "未检测到 Docker，开始安装（用官方 get.docker.com 脚本）"
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
  ok "Docker 安装完成：$(docker --version)"
fi

# ---- 3. 数据目录 ----
say "3/6 创建数据目录 $DATA_DIR"
mkdir -p "$DATA_DIR"/{logs,uploads,ssl}
# 容器内 app 用户 uid:gid 是 100:101
chown -R 100:101 "$DATA_DIR" || warn "chown 失败（可能 SELinux），跳过"
ok "数据目录就绪"

# ---- 4. 下载部署文件 ----
say "4/6 下载 docker-compose + .env.example 到 $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
cd "$INSTALL_DIR"
[[ -f docker-compose.yml ]] && cp docker-compose.yml docker-compose.yml.bak.$(date +%s) && warn "备份了旧 docker-compose.yml"
curl -fsSL "$REPO_RAW/docker-compose.production.yml" -o docker-compose.yml
if [[ -f .env ]]; then
  warn ".env 已存在，跳过下载（保留你现有配置）"
else
  curl -fsSL "$REPO_RAW/.env.example" -o .env
fi
ok "下载完成"

# ---- 5. 自动生成密码 ----
say "5/6 自动生成 6 个强密码"
gen() { openssl rand -hex 32; }

# 只对仍是占位的字段做替换（防覆盖用户手填的真值）
update_if_placeholder() {
  local key=$1; local placeholder=$2; local newval=$3
  if grep -qE "^${key}=${placeholder}$" .env; then
    sed -i.bak "s|^${key}=.*|${key}=${newval}|" .env
    ok "$key 已生成"
  else
    warn "$key 已有非占位值，跳过"
  fi
}

update_if_placeholder MYSQL_ROOT_PASSWORD "please-change-this-strong-password" "$(gen)"
update_if_placeholder MYSQL_PASSWORD "please-change-this-app-password" "$(gen)"
update_if_placeholder REDIS_PASSWORD "please-change-this-redis-password" "$(gen)"
update_if_placeholder JWT_SECRET "please-change-to-64-hex-chars-random-string" "$(gen)"
update_if_placeholder DATA_AES_KEY "please-change-to-exactly-64-hex-chars-aes-256-key-must-be-64hex" "$(gen)"
update_if_placeholder TURN_STATIC_AUTH_SECRET "please-change-to-64-hex-chars-random-string" "$(gen)"
update_if_placeholder ADMIN_BOOTSTRAP_PASSWORD "please-change-this-bootstrap-password" "$(openssl rand -base64 12 | tr -d '=+/')"
rm -f .env.bak

# 自动填入服务器公网 IP（如果还是占位）
if grep -qE "^TURN_EXTERNAL_IP=1\.2\.3\.4$" .env; then
  PUB_IP=$(curl -fsSL --max-time 5 ifconfig.me 2>/dev/null || curl -fsSL --max-time 5 ip.sb 2>/dev/null || echo "")
  if [[ -n "$PUB_IP" ]]; then
    sed -i "s|^TURN_EXTERNAL_IP=.*|TURN_EXTERNAL_IP=${PUB_IP}|" .env
    ok "TURN_EXTERNAL_IP 自动填为 $PUB_IP"
  else
    warn "拿不到公网 IP，请手动改 TURN_EXTERNAL_IP=<你服务器 IP>"
  fi
fi

# ---- 6. 提示 + 启动 ----
say "6/6 启动前必填项检查"
NEED_EDIT=0
grep -qE "^PUBLIC_DOMAIN=cs\.example\.com$" .env && { warn "PUBLIC_DOMAIN 还是占位 cs.example.com，请改成你的域名"; NEED_EDIT=1; }
grep -qE "^ACME_EMAIL=$" .env && { warn "ACME_EMAIL 空，请填你的邮箱（Let's Encrypt 证书申请必填）"; NEED_EDIT=1; }
grep -qE "^TURN_REALM=cs\.example\.com$" .env && { warn "TURN_REALM 还是占位，建议改成跟 PUBLIC_DOMAIN 一致"; NEED_EDIT=1; }

if [[ $NEED_EDIT -eq 1 ]]; then
  warn "请先编辑 $INSTALL_DIR/.env 填好上面这几项，然后执行："
  echo
  echo "  cd $INSTALL_DIR && docker compose pull && docker compose up -d"
  echo
  warn "（暂不自动启动，避免半成品配置）"
  exit 0
fi

say "全部就绪，docker compose pull + up -d 开始拉镜像 + 启动"
docker compose pull
docker compose up -d
sleep 5
docker compose ps

ok "🎉 安装完成"
echo
echo "  管理后台： https://$(grep ^PUBLIC_DOMAIN= .env | cut -d= -f2)/admin/"
echo "  访客 demo：https://$(grep ^PUBLIC_DOMAIN= .env | cut -d= -f2)/widget/demo.html"
echo "  健康检查： https://$(grep ^PUBLIC_DOMAIN= .env | cut -d= -f2)/api/health"
echo "  超管账号： admin / $(grep ^ADMIN_BOOTSTRAP_PASSWORD= .env | cut -d= -f2)"
echo
warn "首次访问管理后台后，记得立刻改超管密码"
warn "完整文档：https://github.com/baofusirys/custom-service/blob/main/INSTALL.md"
