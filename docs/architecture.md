# bushubot 架构设计

> bushubot 是 TGfulibot 的"运维基础设施"：主控后台 + 客户 Agent + 发布流水线。
> 本文档定义 MVP 范围。**没列在这里的功能都不做**。

## 1. 全景图

```
┌──────────────────── 你（主控方）────────────────────┐
│                                                      │
│   主 Bot (TG)  ←──┐         ┌───── GitHub Actions ──┐│
│                    │         │                        ││
│                    ↓         ↓                        ││
│              ┌─────────────────┐    push tag        ││
│              │  master 后台     │    ←── 你          ││
│              │  Gin + GORM + PG │                    ││
│              └────────┬────────┘                     ││
│                       │                              ││
│   GitHub 公开发布仓 ←──┘                              ││
│   TGfulibot-releases                                 ││
│   (Release: tgfulibot-vX.X.tar.gz)                   ││
└──────────────────────┼───────────────────────────────┘
                       │ HTTPS
        ┌──────────────┼──────────────┐
        ↓              ↓              ↓
┌─────────── 客户 1 ─────┐  ┌─── 客户 2 ───┐
│                         │  │              │
│   客户 Bot (TG) ←─→ Agent │  │     ...     │
│                    │     │  │              │
│                    ↓     │  │              │
│              tgfulibot   │  │              │
│              主程序      │  │              │
│              (PG/Redis   │  │              │
│               本地)      │  │              │
└──────────────────────────┘  └──────────────┘
```

## 2. 角色和职责

| 组件 | 部署位置 | 职责 | 技术栈 |
|------|---------|------|--------|
| **master** | 你自己一台 VPS（主控服务器） | 客户管理、Agent API、主 Bot | Go + Gin + GORM + PG |
| **主 Bot** | master 内置（TG Bot 长轮询） | 你这边的命令入口 | telegram-bot-api/v5 |
| **agent** | 每个客户服务器各一份 | 心跳、自动更新、客户 Bot | Go 单二进制 |
| **客户 Bot** | agent 内置（每客户 1 个 Token） | 客户的命令入口 | telegram-bot-api/v5 |
| **TGfulibot 主程序** | 客户服务器 | 业务逻辑（即现有项目） | Go + Gin + GORM |
| **TGfulibot-releases** | GitHub 公开仓 | 编译产物分发 | GitHub Release |

## 3. 数据模型（master 后台）

```sql
CREATE TABLE customers (
    id              SERIAL PRIMARY KEY,
    name            VARCHAR(100) NOT NULL,         -- 你给客户起的别名
    tg_user_id      BIGINT NOT NULL,               -- 客户 TG ID（用于绑定主Bot）
    bot_token       VARCHAR(100) NOT NULL,         -- 客户的 Bot Token
    license_key     VARCHAR(64) UNIQUE NOT NULL,   -- 32位随机码
    server_ip       VARCHAR(45),                   -- Agent 上报
    current_version VARCHAR(32),                   -- Agent 上报
    last_heartbeat_at TIMESTAMPTZ,                 -- Agent 上报
    enabled         BOOLEAN DEFAULT TRUE,          -- 停用开关
    note            TEXT,                          -- 备注
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_customers_license ON customers(license_key);

CREATE TABLE releases (
    id          SERIAL PRIMARY KEY,
    version     VARCHAR(32) UNIQUE NOT NULL,    -- 例如 v0.13
    notes       TEXT,                           -- 更新说明
    is_latest   BOOLEAN DEFAULT FALSE,          -- 是否是当前最新
    published_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE agent_events (
    id            SERIAL PRIMARY KEY,
    customer_id   INTEGER REFERENCES customers(id),
    event_type    VARCHAR(32),                  -- update_started/done/failed
    version       VARCHAR(32),
    error_message TEXT,
    created_at    TIMESTAMPTZ DEFAULT NOW()
);
```

## 4. API 接口（master ↔ agent）

### 4.1 心跳

```
POST /api/v1/agent/heartbeat
Authorization: Bearer <license_key>

请求:
{
  "current_version": "v0.12",
  "hostname": "iZxxx",
  "server_ip": "1.2.3.4"
}

响应 200:
{
  "latest_version": "v0.13",
  "download_url": "https://github.com/.../tgfulibot-v0.13-linux-amd64.tar.gz",
  "enabled": true,
  "message": ""
}

响应 401: license 无效
响应 403: enabled=false → Agent 应停止 tgfulibot 服务
```

### 4.2 上报事件

```
POST /api/v1/agent/report
Authorization: Bearer <license_key>

请求:
{
  "event_type": "update_done",
  "version": "v0.13",
  "error_message": ""
}

响应 200: {}
```

### 4.3 拉取最新通知（推送列队）

```
GET /api/v1/agent/notifications
Authorization: Bearer <license_key>

响应:
{
  "notifications": [
    { "id": 123, "type": "update_available", "version": "v0.13", "message": "..." }
  ]
}
```

> Agent 收到后用客户自己的 Bot Token 把消息发给客户。

## 5. 主 Bot 命令（你这边）

| 命令 | 例子 | 作用 |
|------|------|------|
| `/list` | `/list` | 列出所有客户（在线/离线/版本） |
| `/info` | `/info 老王` | 单客户详情 |
| `/add` | `/add 老王 @username 123456789` | 新增客户（生成 license） |
| `/release` | `/release v0.13 修复xxx` | 标记新版本为最新 |
| `/notify` | `/notify all 系统将在...` | 给所有客户广播 |
| `/disable` | `/disable 老王` | 停用客户 |
| `/enable` | `/enable 老王` | 恢复 |

## 6. 客户 Bot 命令（agent 处理）

| 命令 | 作用 |
|------|------|
| `/start` | 欢迎+当前状态 |
| `/status` | 当前版本、最后心跳时间 |
| `/update` | 立即检查更新 |
| `/restart` | 重启 tgfulibot 主程序 |

## 7. 关键流程

### 7.1 新客户首次部署

```
1. 客户在主 Bot 给你发消息申请
2. 你执行 /add 老王 @username 123456789
   → master 生成 license_key=abc123...
   → 主 Bot 私信发给客户:
     "你的授权码: abc123
      在服务器执行: curl -fsSL https://你域名/install.sh | sudo bash -s -- abc123"
3. 客户复制粘贴到自己服务器
4. install.sh:
   - 装 PG/Redis
   - 下载 tgfulibot-vX.tar.gz + agent.tar.gz
   - 询问客户 Bot Token
   - 写 config.json
   - 启动 tgfulibot.service + agent.service
5. agent 第一次心跳带上 license_key + bot_token + server_ip
   → master 记录到 customers 表
6. 主 Bot 通知你: "客户 老王 上线，IP: 1.2.3.4，版本: v0.13"
```

### 7.2 日常版本更新

```
1. 你 push tag v0.13 到 TGfulibot
2. GitHub Actions 自动:
   - go build -ldflags="-s -w" -trimpath
   - 打包 tar.gz
   - 推到 TGfulibot-releases 仓做 Release
3. 你在主 Bot 执行: /release v0.13 修复xxx
   → master.releases 表 is_latest=true
4. 各客户 agent 下次心跳收到 latest_version=v0.13
   → 自动下载 → 备份旧二进制 → 替换 → systemctl restart tgfulibot
   → 上报 update_done
5. master 收到所有客户更新成功
```

### 7.3 停用跑路客户

```
1. 你执行 /disable 老王
   → customers.enabled=false
2. 客户的 agent 下次心跳收到 enabled=false
   → systemctl stop tgfulibot
   → agent 自己保持运行（这样你想恢复时可以一键开）
```

## 8. 安全策略

| 威胁 | 措施 |
|------|------|
| 客户拿到源码 | CI 编译产物，只下发二进制；ldflags+trimpath；migrations embed |
| license 泄露 | license 一码一服务器，发现重复 IP 上报 → 警告 |
| 主控被入侵 | 数据库 bot_token 用主密钥加密存储（环境变量传入） |
| 中间人攻击 | master 必须 HTTPS（Let's Encrypt + nginx 反代） |
| 客户主程序被反编译 | -s -w -trimpath，关键算法用 garble（暂不做） |

## 9. MVP 边界

✅ **做：**
- master 后台 + 主 Bot + Agent + CI workflow + install.sh
- customers / releases / agent_events 三张表
- 心跳/上报/通知 三个 API

❌ **不做（明确）：**
- 用户付费/计费
- 客户自助购买
- 网页后台（用主 Bot 管理就够）
- 多客户分组/标签
- 灰度发布（一发版所有客户都更新）
- 高可用/集群（master 单机即可）
- 自动开 ECS 服务器（后期再做）

## 10. 部署拓扑（MVP）

```
你（主控方）：
  - 1 台 VPS（master.bushubot.your-domain.com）
    - master 服务（Go）
    - PostgreSQL
    - nginx（HTTPS 反代到 master:8081）
    - 主 Bot 长轮询（master 内置）

每个客户：
  - 1 台 VPS
    - tgfulibot 主程序
    - agent 服务
    - PostgreSQL（tgfulibot 用）
    - Redis（tgfulibot 用）
    - 客户 Bot 长轮询（agent 内置）
```

## 11. 域名约定

| 用途 | 示例 |
|------|------|
| master API | `https://master.bushubot.example.com` |
| install.sh 托管 | `https://install.bushubot.example.com/install.sh` |
| 发布仓库 | `https://github.com/zhangyunhaibot/TGfulibot-releases` |

> 域名 MVP 阶段必须配，否则 Agent 通信走 IP 不安全。
