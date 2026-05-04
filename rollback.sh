#!/usr/bin/env bash
# 用法: sudo bash rollback.sh
# 作用: 回滚到上一个备份的二进制（不动数据库）
# 注意: 如果数据库迁移做了变更，单纯回滚二进制可能跑不起来，请同时考虑 db restore

set -euo pipefail

APP_NAME="tgfulibot"
APP_USER="tgfulibot"
APP_DIR="/opt/${APP_NAME}"
BACKEND_DIR="${APP_DIR}/backend"

red()    { echo -e "\033[31m$*\033[0m"; }
green()  { echo -e "\033[32m$*\033[0m"; }
yellow() { echo -e "\033[33m$*\033[0m"; }

if [[ $EUID -ne 0 ]]; then
  red "请用 root 执行: sudo bash rollback.sh"
  exit 1
fi

cd "${BACKEND_DIR}"

LATEST_BAK=$(ls -t ${APP_NAME}.bak.* 2>/dev/null | head -1 || true)
if [ -z "${LATEST_BAK}" ]; then
  red "没有可用的二进制备份"
  exit 1
fi

yellow "可用备份（最近优先）:"
ls -lh ${APP_NAME}.bak.* | head -5
echo
yellow "将回滚到: ${LATEST_BAK}"
read -p "确认继续? (y/N) " confirm
[[ "${confirm}" =~ ^[Yy]$ ]] || { yellow "已取消"; exit 0; }

TIMESTAMP=$(date +%Y%m%d%H%M%S)

# 当前二进制留个后路
sudo -u "${APP_USER}" mv ${APP_NAME} ${APP_NAME}.failed.${TIMESTAMP}
sudo -u "${APP_USER}" cp ${LATEST_BAK} ${APP_NAME}

systemctl restart "${APP_NAME}"
sleep 3

if systemctl is-active --quiet "${APP_NAME}"; then
  green "✅ 回滚完成（已恢复为 ${LATEST_BAK}）"
  yellow "失败的二进制保留在: ${APP_NAME}.failed.${TIMESTAMP}"
else
  red "❌ 回滚后服务仍异常:"
  journalctl -u "${APP_NAME}" -n 30 --no-pager
  exit 1
fi
