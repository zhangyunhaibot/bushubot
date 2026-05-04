#!/usr/bin/env bash
# backup-master.sh — master 数据库 + 私钥 + 配置每日备份
#
# 用法 (cron 自动跑):
#   0 3 * * * /opt/bushubot/scripts/backup-master.sh >> /var/log/bushubot-backup.log 2>&1
#
# 行为:
#   - pg_dump master 数据库 (custom 格式, 适合 pg_restore)
#   - 备份 master.key + master.pub (master 签发 license 的私钥)
#   - 备份 master config.json (含 db 密码 / bot token)
#   - 打成 master_YYYYMMDD_HHMMSS.tar.gz
#   - 删除 5 天前的备份, 只保留近 5 天
#
# 备份文件包含敏感信息, 权限 600 + bushubot 用户所有

set -euo pipefail

APP_DIR="/opt/bushubot"
MASTER_DIR="${APP_DIR}/master"
BACKUP_DIR="${APP_DIR}/backups"
KEEP_DAYS=5

if [[ ! -f "${MASTER_DIR}/config.json" ]]; then
  echo "❌ 找不到 ${MASTER_DIR}/config.json, 这台机器可能不是 master"
  exit 1
fi

mkdir -p "${BACKUP_DIR}"
TS=$(date +%Y%m%d_%H%M%S)
STAGE="${BACKUP_DIR}/master_${TS}"
mkdir -p "${STAGE}"

# 1. pg_dump 数据库 (custom format, 支持 pg_restore 选择性恢复)
DB_USER=$(jq -r '.database.user' "${MASTER_DIR}/config.json")
DB_PASS=$(jq -r '.database.password' "${MASTER_DIR}/config.json")
DB_NAME=$(jq -r '.database.dbname' "${MASTER_DIR}/config.json")

PGPASSWORD="${DB_PASS}" pg_dump \
  -h localhost \
  -U "${DB_USER}" \
  -d "${DB_NAME}" \
  -F c \
  -f "${STAGE}/db.dump"

# 2. 备份签发 license 的密钥对 (master.key 是私钥, 丢了所有 token 失效)
cp "${MASTER_DIR}/data/keys/master.key" "${STAGE}/master.key"
cp "${MASTER_DIR}/data/keys/master.pub" "${STAGE}/master.pub"

# 3. 备份 config.json (含数据库密码 + bot token)
cp "${MASTER_DIR}/config.json" "${STAGE}/config.json"

# 4. 打 tar.gz
ARCHIVE="${BACKUP_DIR}/master_${TS}.tar.gz"
tar -czf "${ARCHIVE}" -C "${BACKUP_DIR}" "master_${TS}"
rm -rf "${STAGE}"

# 5. 严格权限 (备份文件含私钥 + db 密码)
chmod 600 "${ARCHIVE}"
chown bushubot:bushubot "${ARCHIVE}" 2>/dev/null || true

# 6. 清理 5 天前的备份
DELETED=$(find "${BACKUP_DIR}" -maxdepth 1 -name "master_*.tar.gz" -mtime +${KEEP_DAYS} -print -delete | wc -l)

SIZE=$(stat -c%s "${ARCHIVE}")
echo "[$(date '+%Y-%m-%d %H:%M:%S')] ✅ 备份完成: ${ARCHIVE} (${SIZE} bytes), 清理了 ${DELETED} 个 ${KEEP_DAYS} 天前的旧备份"
