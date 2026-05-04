#!/usr/bin/env bash
# 用法: sudo bash deploy.sh <版本号>
# 例如: sudo bash deploy.sh v0.14
#
# 这是"应急手动部署"脚本——
# 正常情况下 agent 自己会拉新版本，你不需要 SSH 上来跑这个。
# 这个脚本只在以下情况使用:
#   - agent 坏了不会自动更新
#   - 想跳过 master 直接装某个版本验证
#   - 首次部署后想升级到非最新版
#
# 它从 GitHub Release 下载 tar.gz + 校验 sha256 + 替换二进制 + 重启 + 健康检查。
# 跟 agent 自动更新走的同一条路径。

set -euo pipefail

VERSION="${1:-}"

# 严格白名单校验：只接受 v + 数字 + . 形式（防止 ${VERSION} 被注入）
if ! [[ "$VERSION" =~ ^v[0-9]+(\.[0-9]+){0,3}([-a-zA-Z0-9.]*)?$ ]]; then
  echo "用法: sudo bash deploy.sh <版本号>"
  echo "例如: sudo bash deploy.sh v0.14"
  echo "版本号必须形如 v<数字>[.数字...]，不接受其他字符。"
  exit 1
fi

APP_NAME="tgfulibot"
APP_USER="tgfulibot"
APP_DIR="/opt/${APP_NAME}"
BACKEND_DIR="${APP_DIR}/backend"
BACKUP_DIR="${APP_DIR}/backups"
RELEASE_REPO="zhangyunhaibot/TGfulibot-releases"

red()    { echo -e "\033[31m$*\033[0m"; }
green()  { echo -e "\033[32m$*\033[0m"; }
yellow() { echo -e "\033[33m$*\033[0m"; }

if [[ $EUID -ne 0 ]]; then
  red "请用 root 执行: sudo bash deploy.sh ${VERSION}"
  exit 1
fi

if [[ ! -d "${BACKEND_DIR}" ]]; then
  red "找不到 ${BACKEND_DIR}，请先用 install.sh 完成初次部署"
  exit 1
fi

TIMESTAMP=$(date +%Y%m%d%H%M%S)
TMPDIR=$(mktemp -d)
trap 'rm -rf "${TMPDIR}"' EXIT

green "==> [1/6] 下载 ${VERSION}"
URL="https://github.com/${RELEASE_REPO}/releases/download/${VERSION}/${APP_NAME}-${VERSION}-linux-amd64.tar.gz"
curl -fsSL -o "${TMPDIR}/release.tar.gz"        "${URL}"
curl -fsSL -o "${TMPDIR}/release.tar.gz.sha256" "${URL}.sha256"

green "==> [2/6] 校验 sha256"
EXPECTED=$(awk '{print $1}' "${TMPDIR}/release.tar.gz.sha256")
ACTUAL=$(sha256sum "${TMPDIR}/release.tar.gz" | awk '{print $1}')
if [[ "${EXPECTED}" != "${ACTUAL}" ]]; then
  red "❌ sha256 不匹配: 期望 ${EXPECTED}, 实际 ${ACTUAL}"
  exit 1
fi
green "    sha256 OK"

green "==> [3/6] 解压"
tar -xzf "${TMPDIR}/release.tar.gz" -C "${TMPDIR}"
STAGE=$(find "${TMPDIR}" -maxdepth 1 -type d -name "${APP_NAME}-*-linux-amd64" | head -1)
if [[ -z "${STAGE}" ]]; then
  red "❌ 解压结果异常"
  exit 1
fi

green "==> [4/6] 备份数据库 + 备份旧二进制"
mkdir -p "${BACKUP_DIR}"
chown "${APP_USER}:${APP_USER}" "${BACKUP_DIR}"

# 从 config.json 拿数据库密码
if [[ -f "${BACKEND_DIR}/config.json" ]] && command -v jq >/dev/null 2>&1; then
  DB_HOST=$(jq -r '.database.host' "${BACKEND_DIR}/config.json")
  DB_PORT=$(jq -r '.database.port' "${BACKEND_DIR}/config.json")
  DB_USER=$(jq -r '.database.user' "${BACKEND_DIR}/config.json")
  DB_PASS=$(jq -r '.database.password' "${BACKEND_DIR}/config.json")
  DB_NAME=$(jq -r '.database.dbname' "${BACKEND_DIR}/config.json")
  DB_DUMP="${BACKUP_DIR}/db_${TIMESTAMP}.dump"
  PGPASSWORD="${DB_PASS}" pg_dump -h "${DB_HOST}" -p "${DB_PORT}" \
    -U "${DB_USER}" -d "${DB_NAME}" -F c -f "${DB_DUMP}"
  yellow "    数据库备份: ${DB_DUMP}"
else
  yellow "    跳过数据库备份（config.json 或 jq 不存在）"
fi

# 备份旧二进制
if [[ -f "${BACKEND_DIR}/${APP_NAME}" ]]; then
  cp "${BACKEND_DIR}/${APP_NAME}" "${BACKEND_DIR}/${APP_NAME}.bak.${TIMESTAMP}"
fi

green "==> [5/6] 替换二进制 + migrations"
cp "${STAGE}/${APP_NAME}" "${BACKEND_DIR}/${APP_NAME}"
chmod +x "${BACKEND_DIR}/${APP_NAME}"
chown "${APP_USER}:${APP_USER}" "${BACKEND_DIR}/${APP_NAME}"

if [[ -d "${STAGE}/migrations" ]]; then
  rm -rf "${BACKEND_DIR}/migrations"
  cp -r "${STAGE}/migrations" "${BACKEND_DIR}/migrations"
  chown -R "${APP_USER}:${APP_USER}" "${BACKEND_DIR}/migrations"
fi
echo "${VERSION}" > "${BACKEND_DIR}/VERSION"

# 只保留最近 5 份二进制备份
ls -t "${BACKEND_DIR}/${APP_NAME}.bak."* 2>/dev/null | tail -n +6 | xargs -r rm -f
# 只保留最近 10 份数据库备份
ls -t "${BACKUP_DIR}/db_"*.dump 2>/dev/null | tail -n +11 | xargs -r rm -f

green "==> [6/6] 重启服务并健康检查"
systemctl restart "${APP_NAME}"
sleep 3

if journalctl -u "${APP_NAME}" --since "10 seconds ago" | grep -q "服务已启动"; then
  green "✅ 部署成功 - ${VERSION}"
  systemctl status "${APP_NAME}" --no-pager | head -5
else
  red "❌ 服务启动未确认，最近日志:"
  journalctl -u "${APP_NAME}" -n 50 --no-pager
  yellow "如需回滚: sudo bash rollback.sh"
  exit 1
fi
