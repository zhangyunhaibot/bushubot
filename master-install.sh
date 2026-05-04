#!/usr/bin/env bash
# bushubot master 一键部署
#
# 用法:
#   sudo bash master-install.sh \
#     --domain master.qunzhi.name \
#     --email you@example.com \
#     --bot-token <主Bot Token> \
#     --admin-id <你的TG ID>
#
# 适用: Ubuntu 22.04 / 24.04 全新服务器
#
# 完成后会:
#   - 在 8081 端口本地跑 master 服务
#   - nginx 反代 443/80 → 8081，含 Let's Encrypt SSL
#   - 生成 RSA 4096 密钥对（存 /opt/bushubot/master/data/keys/）
#   - 主 Bot 通过长轮询连 Telegram

set -euo pipefail

# ==================== 默认配置 ====================
APP_NAME="bushubot-master"
APP_USER="bushubot"
APP_DIR="/opt/bushubot"
GIT_REPO="https://github.com/zhangyunhaibot/bushubot.git"
DB_NAME="bushubot_master"
DB_USER="bushubot"
GO_VERSION="1.22.5"
RELEASE_REPO_DEFAULT="zhangyunhaibot/TGfulibot-releases"
AGENT_RELEASE_REPO_DEFAULT="zhangyunhaibot/bushubot"
# ===================================================

# ==================== 参数 ====================
DOMAIN=""
ADMIN_EMAIL=""
BOT_TOKEN=""
ADMIN_ID=""
RELEASE_REPO="${RELEASE_REPO_DEFAULT}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain)        DOMAIN="$2"; shift 2 ;;
    --email)         ADMIN_EMAIL="$2"; shift 2 ;;
    --bot-token)     BOT_TOKEN="$2"; shift 2 ;;
    --admin-id)      ADMIN_ID="$2"; shift 2 ;;
    --release-repo)  RELEASE_REPO="$2"; shift 2 ;;
    -h|--help)
      grep '^#' "$0" | head -25 | sed 's/^# \?//'
      exit 0
      ;;
    *) echo "未知选项: $1"; exit 1 ;;
  esac
done

red()    { echo -e "\033[31m$*\033[0m"; }
green()  { echo -e "\033[32m$*\033[0m"; }
yellow() { echo -e "\033[33m$*\033[0m"; }

if [[ $EUID -ne 0 ]]; then
  red "请用 root 执行"
  exit 1
fi

# 必填校验
if [[ -z "$DOMAIN" ]]; then
  red "缺少 --domain（例如 master.qunzhi.name）"; exit 1
fi
if [[ -z "$ADMIN_EMAIL" ]]; then
  red "缺少 --email（用于 Let's Encrypt 通知）"; exit 1
fi
if [[ -z "$BOT_TOKEN" ]]; then
  if [[ -t 0 ]]; then
    read -p "请输入主 Bot Token (来自 @BotFather): " BOT_TOKEN
  else
    red "缺少 --bot-token"; exit 1
  fi
fi
if [[ -z "$ADMIN_ID" ]]; then
  if [[ -t 0 ]]; then
    read -p "请输入你的 Telegram User ID: " ADMIN_ID
  else
    red "缺少 --admin-id"; exit 1
  fi
fi
if ! [[ "$ADMIN_ID" =~ ^[0-9]+$ ]]; then
  red "ADMIN_ID 必须是数字: $ADMIN_ID"; exit 1
fi

# ==================== 1. apt + 依赖 ====================
green "==> [1/12] 系统更新 + 安装基础依赖"
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y git curl wget vim ufw ca-certificates jq build-essential \
                   postgresql nginx certbot python3-certbot-nginx openssl

# ==================== 2. Go ====================
green "==> [2/12] 安装 Go ${GO_VERSION}"
CURRENT_GO=$(go version 2>/dev/null | awk '{print $3}' || echo "none")
if [[ "${CURRENT_GO}" != "go${GO_VERSION}" ]]; then
  cd /tmp
  wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "go${GO_VERSION}.linux-amd64.tar.gz"
  echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
fi
export PATH=$PATH:/usr/local/go/bin
go version

# ==================== 3. 系统用户 ====================
green "==> [3/12] 创建系统用户 ${APP_USER}"
if ! id -u "${APP_USER}" >/dev/null 2>&1; then
  adduser --disabled-password --gecos "" "${APP_USER}"
fi

# ==================== 4. PostgreSQL ====================
green "==> [4/12] 配置 PostgreSQL"
systemctl enable --now postgresql

DB_PASSWORD=$(openssl rand -base64 32 | tr -d '/+=' | cut -c1-32)

sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_user WHERE usename = '${DB_USER}') THEN
    CREATE USER ${DB_USER} WITH PASSWORD '${DB_PASSWORD}';
  ELSE
    ALTER USER ${DB_USER} WITH PASSWORD '${DB_PASSWORD}';
  END IF;
END
\$\$;
SQL

sudo -u postgres psql -tc "SELECT 1 FROM pg_database WHERE datname = '${DB_NAME}'" \
  | grep -q 1 || sudo -u postgres createdb -O "${DB_USER}" "${DB_NAME}"

sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
GRANT ALL PRIVILEGES ON DATABASE ${DB_NAME} TO ${DB_USER};
GRANT ALL ON SCHEMA public TO ${DB_USER};
SQL

# ==================== 5. 拉源码 ====================
green "==> [5/12] 拉取 bushubot 源码"
install -d -o "${APP_USER}" -g "${APP_USER}" "${APP_DIR}"
if [[ -d "${APP_DIR}/.git" ]]; then
  sudo -u "${APP_USER}" bash -c "cd ${APP_DIR} && git fetch --all && git reset --hard origin/main"
else
  sudo -u "${APP_USER}" git clone "${GIT_REPO}" "${APP_DIR}"
fi

# ==================== 6. 编译 ====================
green "==> [6/12] 编译 master"
sudo -u "${APP_USER}" bash <<EOSU
export PATH=\$PATH:/usr/local/go/bin
cd "${APP_DIR}/master"
go build -trimpath -ldflags="-s -w" -o bushubot-master ./cmd
EOSU

# ==================== 7. 数据目录 ====================
green "==> [7/12] 准备数据目录"
install -d -o "${APP_USER}" -g "${APP_USER}" -m 700 "${APP_DIR}/master/data"
install -d -o "${APP_USER}" -g "${APP_USER}" -m 700 "${APP_DIR}/master/data/keys"

# ==================== 8. config.json ====================
green "==> [8/12] 写入配置"
jq -n \
  --arg db_user "${DB_USER}" \
  --arg db_pass "${DB_PASSWORD}" \
  --arg db_name "${DB_NAME}" \
  --arg bot_token "${BOT_TOKEN}" \
  --argjson admin_id "${ADMIN_ID}" \
  --arg release_repo "${RELEASE_REPO}" \
  '{
    server: { port: 8081 },
    database: { host: "localhost", port: 5432, user: $db_user, password: $db_pass, dbname: $db_name },
    telegram_bot: { token: $bot_token, admin_tg_id: $admin_id },
    release_repo: $release_repo,
    license: { keypair_dir: "./data/keys", default_days: 365 }
  }' > "${APP_DIR}/master/config.json"
chown "${APP_USER}:${APP_USER}" "${APP_DIR}/master/config.json"
chmod 600 "${APP_DIR}/master/config.json"

# 也把 agent 仓写到 settings 表（通过迁移自动初始化），这里不做，等启动后用 /set_agent_version 命令配
# 默认值在 003 migration 里就是 zhangyunhaibot/bushubot

# ==================== 9. systemd ====================
green "==> [9/12] systemd 服务"
cat >/etc/systemd/system/bushubot-master.service <<EOF
[Unit]
Description=bushubot master
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=${APP_USER}
Group=${APP_USER}
WorkingDirectory=${APP_DIR}/master
ExecStart=${APP_DIR}/master/bushubot-master --config ${APP_DIR}/master/config.json
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable bushubot-master
systemctl restart bushubot-master
sleep 5

# 健康检查 1
if ! systemctl is-active --quiet bushubot-master; then
  red "❌ master 启动失败，日志:"
  journalctl -u bushubot-master -n 60 --no-pager
  exit 1
fi
green "    master 已运行"

# ==================== 10. nginx ====================
green "==> [10/12] 配置 nginx"
mkdir -p /var/www/html
cat >/etc/nginx/sites-available/bushubot-master <<EOF
server {
    listen 80;
    server_name ${DOMAIN};

    # 给 certbot 留 webroot 验证路径
    location /.well-known/acme-challenge/ {
        root /var/www/html;
    }

    # 反代 master HTTP API
    location / {
        proxy_pass http://127.0.0.1:8081;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 60s;
        client_max_body_size 1m;
    }
}
EOF
ln -sf /etc/nginx/sites-available/bushubot-master /etc/nginx/sites-enabled/
rm -f /etc/nginx/sites-enabled/default
nginx -t
systemctl enable --now nginx
systemctl reload nginx

# ==================== 11. Let's Encrypt ====================
green "==> [11/12] 申请 SSL 证书 ${DOMAIN}"
yellow "    （前提：${DOMAIN} 的 DNS A 记录已生效，且解析到本机 IP）"
certbot --nginx -d "${DOMAIN}" --non-interactive --agree-tos -m "${ADMIN_EMAIL}" --redirect

# certbot 会修改 nginx 配置，自动加 443 server block + redirect
# 确认 reload
nginx -t && systemctl reload nginx

# ==================== 12. 防火墙 ====================
green "==> [12/12] 防火墙"
ufw allow OpenSSH || true
ufw allow 'Nginx Full' || true
ufw --force enable
ufw status

# ==================== 收尾 ====================
sleep 3
HTTPS_OK=false
if curl -fsS --max-time 10 "https://${DOMAIN}/healthz" | grep -q ok; then
  HTTPS_OK=true
fi

if [[ "${HTTPS_OK}" == "true" ]]; then
  green ""
  green "================ 🎉 master 部署完成 ================"
  green "域名:     https://${DOMAIN}"
  green "API:      https://${DOMAIN}/healthz → ok"
  green "服务:     systemctl status bushubot-master"
  green "日志:     journalctl -u bushubot-master -f"
  green ""
  yellow "🔑 master 公钥（要拷贝到 TGfulibot 项目编译时内嵌）："
  echo "----------------------------------------"
  cat "${APP_DIR}/master/data/keys/master.pub" 2>/dev/null
  echo "----------------------------------------"
  green ""
  green "下一步:"
  green "  1. 在 Telegram 找到你刚创建的主 Bot，发 /start"
  green "  2. 用按钮添加第一个客户（或发 /add）"
  green "===================================================="
else
  red "❌ 服务起来了但 HTTPS 测试失败:"
  red "   curl https://${DOMAIN}/healthz 返回:"
  curl -v "https://${DOMAIN}/healthz" 2>&1 | head -20
  exit 1
fi
