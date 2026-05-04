#!/usr/bin/env bash
# upgrade.sh — 客户机本地手动升级 (应急用)
#
# 用法 (在客户机上以 root 跑):
#   curl -fsSL https://raw.githubusercontent.com/zhangyunhaibot/bushubot/main/upgrade.sh | sudo bash -s -- tgfulibot
#   curl -fsSL https://raw.githubusercontent.com/zhangyunhaibot/bushubot/main/upgrade.sh | sudo bash -s -- agent
#
# 用途:
#   - agent 自动升级失败 / agent 自身挂掉之后, 管理员 SSH 进客户机手动升级
#   - 不会动 config.json 和 数据库, 只换 binary (+ tgfulibot 的 migrations 目录)
#   - 重启失败自动回滚到旧版本
#
# 与 install.sh 的区别:
#   install.sh: 完整初装/重置 (装依赖+建库+写 config+systemd+防火墙)
#   upgrade.sh: 只换 binary, 完全保留现有 config + DB + systemd unit + 数据
#
# 与 rollback.sh 的区别:
#   rollback.sh: 把 binary 退回到本地最近一份 .bak (向后)
#   upgrade.sh:  从 GitHub release 拉新版替换 (向前), 失败时自动 fallback 到旧版

set -euo pipefail

APP_NAME="tgfulibot"
APP_USER="bushubot"
APP_DIR="/opt/tgfulibot"
BACKEND_DIR="${APP_DIR}/backend"
AGENT_DIR="${APP_DIR}/agent"
AGENT_BIN_NAME="bushubot-agent"

RELEASE_REPO_TGFULIBOT="zhangyunhaibot/TGfulibot-releases"
RELEASE_REPO_AGENT="zhangyunhaibot/bushubot"

red()    { echo -e "\033[31m$*\033[0m"; }
green()  { echo -e "\033[32m$*\033[0m"; }
yellow() { echo -e "\033[33m$*\033[0m"; }

if [[ $EUID -ne 0 ]]; then
  red "请用 root 执行"
  exit 1
fi

TARGET="${1:-}"
case "${TARGET}" in
  tgfulibot|agent) ;;
  *)
    cat <<EOF
用法: $0 <tgfulibot | agent>

升级 tgfulibot 业务服务:
  curl -fsSL https://raw.githubusercontent.com/zhangyunhaibot/bushubot/main/upgrade.sh | sudo bash -s -- tgfulibot

升级 bushubot-agent 监控代理:
  curl -fsSL https://raw.githubusercontent.com/zhangyunhaibot/bushubot/main/upgrade.sh | sudo bash -s -- agent
EOF
    exit 1
    ;;
esac

# ---------- helpers ----------
download_and_verify() {
  local url="$1"
  local dst="$2"
  green "    下载: ${url}"
  if ! curl -fsSL --max-time 300 -o "${dst}" "${url}"; then
    red "    下载失败"
    return 1
  fi
  green "    完成 ($(stat -c%s "${dst}") bytes), 校验 sha256..."
  local expected
  expected=$(curl -fsSL --max-time 30 "${url}.sha256" | awk '{print $1}')
  if [[ -z "${expected}" ]]; then
    red "    sha256 文件为空"
    return 1
  fi
  local got
  got=$(sha256sum "${dst}" | awk '{print $1}')
  if [[ "${expected}" != "${got}" ]]; then
    red "    sha256 不匹配: 期望 ${expected}, 实际 ${got}"
    return 1
  fi
  green "    sha256 OK"
}

TMPDIR=$(mktemp -d)
trap "rm -rf ${TMPDIR}" EXIT
TS=$(date +%Y%m%d_%H%M%S)

# ---------- tgfulibot ----------
upgrade_tgfulibot() {
  green "==> 升级 tgfulibot (业务服务)"

  if [[ ! -f "${BACKEND_DIR}/${APP_NAME}" ]]; then
    red "未检测到 ${BACKEND_DIR}/${APP_NAME}, 这台机器还没初装过, 请先跑 install.sh"
    exit 1
  fi

  green "    拉取最新版本号 (${RELEASE_REPO_TGFULIBOT})..."
  local latest
  latest=$(curl -fsSL --max-time 30 "https://api.github.com/repos/${RELEASE_REPO_TGFULIBOT}/releases/latest" | jq -r '.tag_name // empty')
  if [[ -z "${latest}" ]]; then
    red "拉取最新版本号失败 (检查网络 + 仓库是否有 release)"
    exit 1
  fi

  local current="-"
  [[ -f "${BACKEND_DIR}/VERSION" ]] && current=$(cat "${BACKEND_DIR}/VERSION" | tr -d '[:space:]')
  green "    当前版本: ${current}"
  green "    最新版本: ${latest}"

  if [[ "${current}" == "${latest}" ]]; then
    yellow "    已经是最新版, 无需升级 (强制重装请用 install.sh --keep-config)"
    exit 0
  fi

  local url="https://github.com/${RELEASE_REPO_TGFULIBOT}/releases/download/${latest}/${APP_NAME}-${latest}-linux-amd64.tar.gz"
  download_and_verify "${url}" "${TMPDIR}/release.tar.gz"

  tar -xzf "${TMPDIR}/release.tar.gz" -C "${TMPDIR}"
  local stage
  stage=$(find "${TMPDIR}" -maxdepth 1 -type d -name "${APP_NAME}-*-linux-amd64" | head -1)
  if [[ -z "${stage}" ]]; then
    red "解压结果异常: 找不到 ${APP_NAME}-*-linux-amd64 目录"
    exit 1
  fi

  green "    备份当前 binary 和 migrations (TS=${TS})..."
  mv "${BACKEND_DIR}/${APP_NAME}" "${BACKEND_DIR}/${APP_NAME}.bak.${TS}"
  if [[ -d "${BACKEND_DIR}/migrations" ]]; then
    mv "${BACKEND_DIR}/migrations" "${BACKEND_DIR}/migrations.bak.${TS}"
  fi

  green "    替换 binary + migrations..."
  cp "${stage}/${APP_NAME}" "${BACKEND_DIR}/${APP_NAME}"
  cp -r "${stage}/migrations" "${BACKEND_DIR}/migrations"
  echo "${latest}" > "${BACKEND_DIR}/VERSION"
  chown -R "${APP_USER}:${APP_USER}" "${BACKEND_DIR}/${APP_NAME}" "${BACKEND_DIR}/migrations" "${BACKEND_DIR}/VERSION"
  chmod +x "${BACKEND_DIR}/${APP_NAME}"

  green "    重启 ${APP_NAME}.service..."
  if systemctl restart "${APP_NAME}.service" 2>/dev/null; then
    sleep 3
    if systemctl is-active --quiet "${APP_NAME}.service"; then
      green "✅ tgfulibot 升级成功: ${current} → ${latest}"
      green "   旧版本备份: ${BACKEND_DIR}/${APP_NAME}.bak.${TS}"
      exit 0
    fi
  fi

  # ---------- 失败回滚 ----------
  red "❌ 重启失败, 自动回滚到旧版本..."
  rm -f "${BACKEND_DIR}/${APP_NAME}"
  rm -rf "${BACKEND_DIR}/migrations"
  mv "${BACKEND_DIR}/${APP_NAME}.bak.${TS}" "${BACKEND_DIR}/${APP_NAME}"
  if [[ -d "${BACKEND_DIR}/migrations.bak.${TS}" ]]; then
    mv "${BACKEND_DIR}/migrations.bak.${TS}" "${BACKEND_DIR}/migrations"
  fi
  echo "${current}" > "${BACKEND_DIR}/VERSION"
  systemctl restart "${APP_NAME}.service" || true
  red "已回滚, 请检查: journalctl -u ${APP_NAME} -n 50 --no-pager"
  exit 1
}

# ---------- agent ----------
upgrade_agent() {
  green "==> 升级 bushubot-agent (监控代理)"

  if [[ ! -f "${AGENT_DIR}/${AGENT_BIN_NAME}" ]]; then
    red "未检测到 ${AGENT_DIR}/${AGENT_BIN_NAME}, 这台机器还没初装过, 请先跑 install.sh"
    exit 1
  fi

  green "    拉取最新 agent 版本号 (${RELEASE_REPO_AGENT})..."
  # agent tag 形如 agent-v0.1, 找最新一个
  local latest
  latest=$(curl -fsSL --max-time 30 "https://api.github.com/repos/${RELEASE_REPO_AGENT}/releases" | jq -r '[.[] | select(.tag_name | startswith("agent-v"))][0].tag_name // empty')
  if [[ -z "${latest}" ]]; then
    red "拉取最新 agent-v* tag 失败 (仓库可能还没有 agent release)"
    exit 1
  fi
  green "    最新版本: ${latest}"

  local url="https://github.com/${RELEASE_REPO_AGENT}/releases/download/${latest}/${AGENT_BIN_NAME}-${latest}-linux-amd64.tar.gz"
  download_and_verify "${url}" "${TMPDIR}/agent.tar.gz"

  tar -xzf "${TMPDIR}/agent.tar.gz" -C "${TMPDIR}"
  local stage
  stage=$(find "${TMPDIR}" -maxdepth 1 -type d -name "${AGENT_BIN_NAME}-*-linux-amd64" | head -1)
  if [[ -z "${stage}" ]]; then
    red "解压结果异常: 找不到 ${AGENT_BIN_NAME}-*-linux-amd64 目录"
    exit 1
  fi

  green "    备份当前 agent (TS=${TS})..."
  mv "${AGENT_DIR}/${AGENT_BIN_NAME}" "${AGENT_DIR}/${AGENT_BIN_NAME}.bak.${TS}"

  green "    替换 binary..."
  cp "${stage}/${AGENT_BIN_NAME}" "${AGENT_DIR}/${AGENT_BIN_NAME}"
  chown "${APP_USER}:${APP_USER}" "${AGENT_DIR}/${AGENT_BIN_NAME}"
  chmod +x "${AGENT_DIR}/${AGENT_BIN_NAME}"

  green "    重启 ${AGENT_BIN_NAME}.service..."
  if systemctl restart "${AGENT_BIN_NAME}.service" 2>/dev/null; then
    sleep 3
    if systemctl is-active --quiet "${AGENT_BIN_NAME}.service"; then
      green "✅ agent 升级成功 → ${latest}"
      green "   旧版本备份: ${AGENT_DIR}/${AGENT_BIN_NAME}.bak.${TS}"
      exit 0
    fi
  fi

  # ---------- 失败回滚 ----------
  red "❌ 重启失败, 自动回滚到旧版本..."
  rm -f "${AGENT_DIR}/${AGENT_BIN_NAME}"
  mv "${AGENT_DIR}/${AGENT_BIN_NAME}.bak.${TS}" "${AGENT_DIR}/${AGENT_BIN_NAME}"
  systemctl restart "${AGENT_BIN_NAME}.service" || true
  red "已回滚, 请检查: journalctl -u ${AGENT_BIN_NAME} -n 50 --no-pager"
  exit 1
}

case "${TARGET}" in
  tgfulibot) upgrade_tgfulibot ;;
  agent)     upgrade_agent ;;
esac
