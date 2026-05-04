#!/usr/bin/env bash
# 用法 1 (cloud-init / 全自动):
#   sudo bash install.sh \
#     --license '<token>' \
#     --bot-token '<bot_token>' \
#     --admin-id <tg_id>
#
# 用法 2 (SSH 交互):
#   sudo bash install.sh '<license_token>'
#   （会提示交互输入 bot_token / admin_id）
#
# 适用: Ubuntu 22.04 / 24.04 全新服务器
# 作用: 装环境 + 下载已编译的 tgfulibot 主程序 + 装 agent
# 不会做的事:
#   - 不再 git clone（客户拿不到源码）
#   - 不再装 Go（不需要本地编译）

set -euo pipefail

# ==================== 配置区（你部署前要改）====================
APP_NAME="tgfulibot"
APP_USER="tgfulibot"
APP_DIR="/opt/${APP_NAME}"
BACKEND_DIR="${APP_DIR}/backend"
AGENT_DIR="${APP_DIR}/agent"

DB_NAME="telegram_bot"
DB_USER="tgfulibot"

# 主控地址（默认值，可被 --master-url 覆盖）
MASTER_URL="https://master.qunzhi.name"

# 发布仓 (公开仓 owner/name)
RELEASE_REPO_TGFULIBOT="zhangyunhaibot/TGfulibot-releases"
RELEASE_REPO_AGENT="zhangyunhaibot/bushubot"
# ===============================================================

red()    { echo -e "\033[31m$*\033[0m"; }
green()  { echo -e "\033[32m$*\033[0m"; }
yellow() { echo -e "\033[33m$*\033[0m"; }

# 下载 url + 同步下载 .sha256，校验通过后才返回成功
# 用法: download_and_verify <url> <out_path>
download_and_verify() {
  local url="$1"
  local out="$2"
  curl -fsSL --max-time 300 -o "$out"          "$url"          || return 1
  curl -fsSL --max-time 30  -o "${out}.sha256" "${url}.sha256" || {
    red "❌ 下载 sha256 文件失败: ${url}.sha256"
    return 1
  }
  local expected actual
  expected=$(awk '{print $1}' "${out}.sha256")
  actual=$(sha256sum "$out" | awk '{print $1}')
  if [[ "$expected" != "$actual" ]]; then
    red "❌ sha256 不匹配:"
    red "   期望: $expected"
    red "   实际: $actual"
    red "   下载链路可能损坏或被篡改，停止安装"
    return 1
  fi
  rm -f "${out}.sha256"
  return 0
}

# ==================== 参数解析 ====================
LICENSE_TOKEN=""
BOT_TOKEN=""
ADMIN_ID=""

show_help() {
  cat <<EOF
用法:
  cloud-init / 全自动:
    sudo bash install.sh --license '<token>' --bot-token '<bot_token>' --admin-id <tg_id> [--master-url <url>]
  SSH 交互:
    sudo bash install.sh '<license_token>'
    （会交互式询问 bot_token 和 admin_id）

参数:
  --license      master 签发的授权 token
  --bot-token    客户自己的 Telegram Bot Token
  --admin-id     客户的 Telegram user ID (数字)
  --master-url   master 服务器地址 (默认 ${MASTER_URL})
  -h, --help     显示帮助
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --license)    LICENSE_TOKEN="$2"; shift 2 ;;
    --bot-token)  BOT_TOKEN="$2"; shift 2 ;;
    --admin-id)   ADMIN_ID="$2"; shift 2 ;;
    --master-url) MASTER_URL="$2"; shift 2 ;;
    -h|--help)    show_help; exit 0 ;;
    --) shift; break ;;
    -*) red "未知选项: $1"; show_help; exit 1 ;;
    *)
      # 兼容旧的位置参数：第一个无 -- 的参数当 license
      if [[ -z "$LICENSE_TOKEN" ]]; then
        LICENSE_TOKEN="$1"
      fi
      shift
      ;;
  esac
done
# ===================================================

if [[ $EUID -ne 0 ]]; then
  red "请用 root 执行"
  show_help
  exit 1
fi

if [[ -z "$LICENSE_TOKEN" ]]; then
  red "缺少 --license 参数"
  show_help
  exit 1
fi

green "==> [1/9] 更新系统并安装基础依赖"
apt-get update -y
apt-get install -y git curl wget vim ufw ca-certificates jq tar \
                   postgresql redis-server openssl

green "==> [2/9] 创建系统用户 ${APP_USER}"
if ! id -u "${APP_USER}" >/dev/null 2>&1; then
  adduser --disabled-password --gecos "" "${APP_USER}"
fi

green "==> [3/9] 配置 PostgreSQL + Redis"
systemctl enable --now postgresql redis-server

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
SQL

green "==> [4/9] 下载 tgfulibot 最新版本"
install -d -o "${APP_USER}" -g "${APP_USER}" "${APP_DIR}" "${BACKEND_DIR}" "${AGENT_DIR}"

LATEST_TGFULIBOT=$(curl -fsSL "https://api.github.com/repos/${RELEASE_REPO_TGFULIBOT}/releases/latest" | jq -r .tag_name)
if [[ -z "${LATEST_TGFULIBOT}" || "${LATEST_TGFULIBOT}" == "null" ]]; then
  red "获取最新版本失败"
  exit 1
fi
green "    最新版本: ${LATEST_TGFULIBOT}"

TMPDIR=$(mktemp -d)
TGFUL_URL="https://github.com/${RELEASE_REPO_TGFULIBOT}/releases/download/${LATEST_TGFULIBOT}/${APP_NAME}-${LATEST_TGFULIBOT}-linux-amd64.tar.gz"
download_and_verify "${TGFUL_URL}" "${TMPDIR}/tgfulibot.tar.gz" || { rm -rf "${TMPDIR}"; exit 1; }
green "    sha256 OK"
tar -xzf "${TMPDIR}/tgfulibot.tar.gz" -C "${TMPDIR}"
STAGE=$(find "${TMPDIR}" -maxdepth 1 -type d -name "${APP_NAME}-*-linux-amd64" | head -1)
cp "${STAGE}/${APP_NAME}" "${BACKEND_DIR}/${APP_NAME}"
cp -r "${STAGE}/migrations" "${BACKEND_DIR}/migrations"
echo "${LATEST_TGFULIBOT}" > "${BACKEND_DIR}/VERSION"
chown -R "${APP_USER}:${APP_USER}" "${BACKEND_DIR}"
chmod +x "${BACKEND_DIR}/${APP_NAME}"

green "==> [5/9] 下载 agent 最新版本"
LATEST_AGENT=$(curl -fsSL "https://api.github.com/repos/${RELEASE_REPO_AGENT}/releases/latest" | jq -r '.tag_name // empty')
if [[ -z "${LATEST_AGENT}" ]]; then
  red "❌ 拉取 agent release 失败（仓库 ${RELEASE_REPO_AGENT} 没有 release）"
  red ""
  red "agent 是核心组件，没有它整个远程运维体系都失效:"
  red "  - 远程吊销 (停用客户) 失效"
  red "  - 自动更新失效"
  red "  - 日志/指标/告警全部失效"
  red ""
  red "请先在 bushubot 仓 push 一个 agent-v* tag 触发 CI 出包，然后再跑这个脚本。"
  exit 1
fi
green "    最新版本: ${LATEST_AGENT}"
AGENT_URL="https://github.com/${RELEASE_REPO_AGENT}/releases/download/${LATEST_AGENT}/bushubot-agent-${LATEST_AGENT}-linux-amd64.tar.gz"
download_and_verify "${AGENT_URL}" "${TMPDIR}/agent.tar.gz" || { rm -rf "${TMPDIR}"; exit 1; }
green "    sha256 OK"
tar -xzf "${TMPDIR}/agent.tar.gz" -C "${TMPDIR}"
AGENT_STAGE=$(find "${TMPDIR}" -maxdepth 1 -type d -name "bushubot-agent-*-linux-amd64" | head -1)
cp "${AGENT_STAGE}/bushubot-agent" "${AGENT_DIR}/bushubot-agent"
chown -R "${APP_USER}:${APP_USER}" "${AGENT_DIR}"
chmod +x "${AGENT_DIR}/bushubot-agent"

rm -rf "${TMPDIR}"

green "==> [6/9] 写入配置"

# 缺参数：有 tty 就交互问，没 tty (cloud-init 模式) 就报错退出
if [[ -z "$BOT_TOKEN" ]]; then
  if [[ -t 0 ]]; then
    read -p "请输入 Telegram Bot Token (来自 @BotFather): " BOT_TOKEN
  else
    red "缺少 --bot-token 参数（cloud-init 模式必须命令行传入）"
    exit 1
  fi
fi
if [[ -z "$ADMIN_ID" ]]; then
  if [[ -t 0 ]]; then
    read -p "请输入 Telegram 用户 ID (数字): " ADMIN_ID
  else
    red "缺少 --admin-id 参数（cloud-init 模式必须命令行传入）"
    exit 1
  fi
fi
# 校验 admin_id 是数字
if ! [[ "$ADMIN_ID" =~ ^[0-9]+$ ]]; then
  red "ADMIN_ID 必须是数字: $ADMIN_ID"
  exit 1
fi

JWT_SECRET=$(openssl rand -base64 48 | tr -d '/+=' | cut -c1-48)
INTERNAL_API_KEY=$(openssl rand -base64 48 | tr -d '/+=' | cut -c1-48)

# tgfulibot 主程序配置（同时把 license token 写进 license.token，等 TGfulibot 接入后会校验）
jq -n \
  --arg db_user "${DB_USER}" \
  --arg db_pass "${DB_PASSWORD}" \
  --arg db_name "${DB_NAME}" \
  --arg bot_token "${BOT_TOKEN}" \
  --argjson admin_id "${ADMIN_ID}" \
  --arg jwt_secret "${JWT_SECRET}" \
  --arg api_key "${INTERNAL_API_KEY}" \
  --arg license_token "${LICENSE_TOKEN}" \
  --arg master_url "${MASTER_URL}" \
  '{
    server: { port: 8080, allowed_origins: ["http://localhost:5173"] },
    database: { host: "localhost", port: 5432, user: $db_user, password: $db_pass, dbname: $db_name },
    redis: { host: "localhost", port: 6379, password: "", db: 0 },
    telegram_bot: { token: $bot_token, debug: false, admin_id: $admin_id },
    bot_token: $bot_token,
    jwt: { secret: $jwt_secret, expire_hours: 24 },
    internal_api_key: $api_key,
    admin_ids: [$admin_id],
    license: { token: $license_token, master_url: $master_url }
  }' > "${BACKEND_DIR}/config.json"
chown "${APP_USER}:${APP_USER}" "${BACKEND_DIR}/config.json"
chmod 600 "${BACKEND_DIR}/config.json"

# agent 配置
jq -n \
  --arg master_url "${MASTER_URL}" \
  --arg license "${LICENSE_TOKEN}" \
  --arg bot_token "${BOT_TOKEN}" \
  --argjson owner_id "${ADMIN_ID}" \
  --arg app_name "${APP_NAME}" \
  --arg app_dir "${APP_DIR}" \
  --arg binary "${BACKEND_DIR}/${APP_NAME}" \
  --arg service "${APP_NAME}.service" \
  --arg version_file "${BACKEND_DIR}/VERSION" \
  '{
    master_url: $master_url,
    license_key: $license,
    bot_token: $bot_token,
    owner_tg_id: $owner_id,
    heartbeat_interval_seconds: 60,
    grace_days_offline: 7,
    app_name: $app_name,
    app_dir: $app_dir,
    binary_path: $binary,
    service_name: $service,
    version_file: $version_file
  }' > "${AGENT_DIR}/config.json"
chown "${APP_USER}:${APP_USER}" "${AGENT_DIR}/config.json"
chmod 600 "${AGENT_DIR}/config.json"

green "==> [7/9] 写入 systemd 服务"
# tgfulibot 主程序
cat >"/etc/systemd/system/${APP_NAME}.service" <<EOF
[Unit]
Description=${APP_NAME} Telegram Bot
After=network-online.target postgresql.service redis-server.service
Wants=network-online.target

[Service]
Type=simple
User=${APP_USER}
Group=${APP_USER}
WorkingDirectory=${BACKEND_DIR}
ExecStart=${BACKEND_DIR}/${APP_NAME}
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

# agent
cat >"/etc/systemd/system/bushubot-agent.service" <<EOF
[Unit]
Description=bushubot agent (auto-update + remote control)
After=network-online.target ${APP_NAME}.service
Wants=network-online.target

[Service]
Type=simple
# agent 需要 systemctl restart 主服务，因此用 root 运行
User=root
WorkingDirectory=${AGENT_DIR}
ExecStart=${AGENT_DIR}/bushubot-agent --config ${AGENT_DIR}/config.json
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "${APP_NAME}" bushubot-agent
systemctl restart "${APP_NAME}"
systemctl restart bushubot-agent

green "==> [8/9] 配置防火墙 + 备份目录"
ufw allow OpenSSH || true
ufw --force enable
install -d -o "${APP_USER}" -g "${APP_USER}" "${APP_DIR}/backups"

green "==> [9/9] 健康检查"
sleep 5
TGFUL_OK=false
AGENT_OK=false
systemctl is-active --quiet "${APP_NAME}" && TGFUL_OK=true
systemctl is-active --quiet bushubot-agent && AGENT_OK=true

if [[ "${TGFUL_OK}" == "true" && "${AGENT_OK}" == "true" ]]; then
  green ""
  green "================ 部署完成 ================"
  green "tgfulibot 版本: ${LATEST_TGFULIBOT}"
  green "agent 版本:     ${LATEST_AGENT}"
  green ""
  green "查看主程序日志: journalctl -u ${APP_NAME} -f"
  green "查看 agent 日志: journalctl -u bushubot-agent -f"
  green "=========================================="
else
  red "❌ 部分服务启动异常，请查看:"
  [[ "${TGFUL_OK}" != "true" ]] && journalctl -u "${APP_NAME}" -n 30 --no-pager
  [[ "${AGENT_OK}" != "true" ]] && journalctl -u bushubot-agent -n 30 --no-pager
  exit 1
fi
