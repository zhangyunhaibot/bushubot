# Master 端到端部署 SOP

> 你（管理员）部署一台 master 控制台。完成后通过 Telegram 主 Bot 管理所有客户。

## 前置准备

1. **服务器**：1 台 Ubuntu 22.04/24.04，至少 1C2G，公网 IP
2. **域名**：一个二级域名（例如 `master.qunzhi.name`），A 记录指向服务器 IP
3. **主 Bot**：在 [@BotFather](https://t.me/BotFather) 创建一个 Bot，记下 Token
4. **TG ID**：给 [@userinfobot](https://t.me/userinfobot) 发任意消息，记下你的数字 ID
5. **邮箱**：用于 Let's Encrypt 通知（域名快过期时邮件提醒）

---

## 一键部署

SSH 到 master 服务器后执行：

```bash
curl -fsSL https://raw.githubusercontent.com/zhangyunhaibot/bushubot/main/master-install.sh \
  -o master-install.sh

sudo bash master-install.sh \
  --domain master.qunzhi.name \
  --email you@example.com \
  --bot-token "<主Bot Token>" \
  --admin-id <你的TG ID>
```

脚本会自动完成 12 步：

1. apt 更新 + 装基础依赖（PG/nginx/certbot/Go 等）
2. 装 Go 1.22.5
3. 创建 `bushubot` 系统用户
4. 配置 PostgreSQL（库：`bushubot_master`，密码自动生成）
5. clone bushubot 源码到 `/opt/bushubot`
6. 编译 master 二进制（`-trimpath -ldflags="-s -w"`）
7. 准备数据目录 `/opt/bushubot/master/data/keys`（700 权限）
8. 写 `config.json`
9. 写 systemd 服务，启动 master
10. 配置 nginx 反代 80 → 8081
11. 申请 Let's Encrypt SSL 证书 + 强制 HTTPS
12. 配置 ufw（只放 SSH + Nginx）

完成后会打印 master 公钥（拷贝到 TGfulibot 项目编译时内嵌用）。

---

## 部署成功验证

### 1. 服务状态

```bash
systemctl status bushubot-master
journalctl -u bushubot-master -n 30 --no-pager
```

应看到：
```
2026/05/04 ... 数据库连接成功
2026/05/04 ... 执行迁移: 001_init.sql
2026/05/04 ... 执行迁移: 002_broadcasts.sql
2026/05/04 ... 执行迁移: 003_metrics_logs.sql
2026/05/04 ... 执行迁移: 004_metrics_history.sql
2026/05/04 ... 执行迁移: 005_notification_ack.sql
2026/05/04 ... 执行迁移: 006_customer_name_unique.sql
2026/05/04 ... master 公钥已就绪 ...
2026/05/04 ... HTTP API 监听 :8081
2026/05/04 ... master Bot 已就绪
```

### 2. HTTPS

```bash
curl https://master.qunzhi.name/healthz
# 应返回: ok
```

### 3. 主 Bot

打开 Telegram，找到你的主 Bot，发 `/start` —— 应看到主菜单按钮。

---

## 第一次添加客户（端到端测试）

### Step 1：在主 Bot 里 /add

主菜单 →【👥 客户管理】→【➕ 添加客户】，按提示填：

| 步骤 | 输入 |
|------|------|
| 1/4 客户名 | `测试王`（你给客户起的别名） |
| 2/4 TG ID | 客户的 Telegram User ID |
| 3/4 Bot Token | 客户自己创建的 Bot Token |
| 4/4 备注 | `第一个测试客户`（不需要发 -） |

主 Bot 返回：
```
✅ 已创建客户 测试王（有效期至 2027-05-04）

方式 1 · Cloud-init: <一段脚本>
方式 2 · SSH 手工执行: <一段命令>
授权 token: eyJj...
```

### Step 2：把客户安装命令发给客户（或自己跑去测试机）

在测试客户端服务器（`47.82.232.195`）执行**方式 2 · SSH 手工执行**那段：

```bash
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zhangyunhaibot/bushubot/main/install.sh)" -- \
  --license '<token>' \
  --bot-token '<客户Bot Token>' \
  --admin-id <客户TG ID>
```

注意：install.sh 默认 `MASTER_URL=https://master.qunzhi.name` 已经写好，客户拷主 Bot 给的命令直接跑就行。

### Step 3：等 5 分钟看上线

- 客户端 install.sh 完成后会启动 `tgfulibot.service` 和 `bushubot-agent.service`
- agent 在 60 秒内做第一次心跳到你的 master
- 主 Bot 收到推送：

```
🟢 客户 测试王 上线
   IP: 47.82.232.195
   tgfulibot: v0.50
   agent: agent-v0.1
```

> 上线推送目前依赖 alerter——MVP 阶段还没做"上线通知"事件，会通过 `/list` 看到状态变成🟢。后续可以加 onCustomerOnline 事件。

### Step 4：在主 Bot 里查询客户状态

`/list` 看列表，`/info 测试王` 看详情，含：
- 服务器 IP / 当前版本 / 最后心跳
- 资源指标（内存/磁盘/负载）
- 操作按钮（重启 / 立即更新 / 查看日志 / 历史曲线）

---

## 常见运维操作

### 升级 master 自身

```bash
ssh root@master.qunzhi.name
cd /opt/bushubot
sudo -u bushubot git pull
sudo -u bushubot bash -c '
    export PATH=$PATH:/usr/local/go/bin
    cd master
    go build -trimpath -ldflags="-s -w" -o bushubot-master.new ./cmd
    mv bushubot-master bushubot-master.bak.$(date +%Y%m%d%H%M%S)
    mv bushubot-master.new bushubot-master
'
sudo systemctl restart bushubot-master
sudo journalctl -u bushubot-master -n 50 --no-pager
```

### 备份 master 数据库

```bash
PGPASSWORD=$(jq -r .database.password /opt/bushubot/master/config.json) \
  pg_dump -h localhost -U bushubot -d bushubot_master -F c \
  -f /opt/bushubot/backups/master_$(date +%Y%m%d_%H%M%S).dump
```

强烈建议加到 cron，每天一次：
```bash
0 3 * * * /opt/bushubot/scripts/backup-master.sh
```

### 备份 master 私钥（**最重要**）

```bash
# 私钥所在
/opt/bushubot/master/data/keys/master.key

# 备份到本地
scp root@master.qunzhi.name:/opt/bushubot/master/data/keys/master.key ~/safe-place/
```

**这个文件丢了 = 所有 license token 失效，所有客户必须重新签发**。
丢失后唯一恢复办法：
1. 生成新密钥
2. 把新公钥编进 TGfulibot 新版
3. 客户全部升级 TGfulibot
4. 给所有客户重新签发 token

所以这个文件**至少备份到 3 个地方**（云盘、U 盘、密码管理器加密）。

### 远程吊销跑路客户

主 Bot 发 `/disable 客户名` 或按按钮 → 停用。客户 agent 下次心跳收到 enabled=false → stop tgfulibot.service。

### 强制升级所有客户

```
/release v0.51 紧急修复 xxx
```

所有 enabled 客户 agent ≤60s 自动拉新版升级。

---

## 故障排查

### master 启动失败

```bash
journalctl -u bushubot-master -n 100 --no-pager
```

常见错误：
- **数据库连接失败** → 检查 `config.json` 的 db 密码、PG 是否在跑（`systemctl status postgresql`）
- **Bot Token 无效** → 主 Bot Token 写错了；从 BotFather 重新查
- **migrations 失败** → 数据库 schema 跟代码不一致，看具体哪个文件失败

### nginx 502 Bad Gateway

```bash
systemctl status bushubot-master
# 如果未跑 → 上面那一步排查
# 如果跑了 → 检查 master 监听端口
ss -tlnp | grep 8081
```

### Let's Encrypt 申请失败

```bash
# 必须域名 DNS 已生效
dig master.qunzhi.name +short
# 必须返回你服务器 IP
```

如果 DNS 没生效，等几分钟再跑：
```bash
sudo certbot --nginx -d master.qunzhi.name
```

### SSL 证书续期

certbot 会自动续期（每天 cron 跑 `certbot renew`），证书到期前 30 天自动换。验证续期跑：
```bash
sudo certbot renew --dry-run
```

---

## 安全清单

部署完成后**逐项检查**：

- [ ] `master.key` 权限 `600`：`ls -l /opt/bushubot/master/data/keys/master.key`
- [ ] `config.json` 权限 `600`：`ls -l /opt/bushubot/master/config.json`
- [ ] PostgreSQL 只监听 `127.0.0.1`：`ss -tlnp | grep 5432` 不应有 `0.0.0.0`
- [ ] master HTTP API 只通过 nginx：`ss -tlnp | grep 8081` 应 bind `127.0.0.1` 或 `::1`
- [ ] ufw 已启用：`ufw status` 应是 `active`
- [ ] HTTPS 强制：`curl -I http://master.qunzhi.name` 应 301 到 https
- [ ] master.key 备份到 master 服务器外（云盘 + U 盘）
- [ ] config.json 已备份（含数据库密码）

---

## 暂未做、后期再加

- 监控 metrics 暴露（Prometheus endpoint）
- master 自动升级（现在要 SSH 手动 git pull + 编译）
- 多 master 高可用
- bot_token 加密存储（master 数据库当前明文存）

这些等线上跑稳定后再考虑。
