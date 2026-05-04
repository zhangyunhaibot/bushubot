# 协议演化纪律

> bushubot 的 master ↔ agent 协议是**合同**。
> 客户服务器上的旧版 agent 可能跑很久（你说不动客户重装），所以协议必须能向前向后演化，**不能破坏老客户**。

## 1. 三条铁律

### 🟢 允许做

1. **加新字段**到请求/响应（旧 agent 自动忽略）
2. **加新 endpoint**（旧 agent 不调用即可）
3. **加新 settings 项**

### 🔴 禁止做

1. **删除现有字段**
2. **修改现有字段的语义**（"version" 永远是字符串，不能突然变成数字 ID）
3. **修改现有字段的类型**
4. **修改 HTTP 方法或 path**
5. **修改 HTTP 状态码语义**

如果真的必须破坏性变更：开新 endpoint `/api/v2/...`，让 v1 继续活着，等所有 agent 都升级到调 v2 后再下线 v1（一般 ≥ 6 个月）。

## 2. 版本演化的关键基础设施（已实现）

### 2.1 Agent 上报自己的版本

```json
POST /api/v1/agent/heartbeat
{
  "agent_version": "agent-v0.1",
  "current_version": "v0.13",
  ...
}
```

→ master 数据库 `customers.agent_version` 字段记录。
→ 你能在 `/list` 看到每个客户的 agent 版本。

### 2.2 Master 下发最新 agent 版本

```json
{
  "latest_agent_version": "agent-v0.2",
  "agent_download_url": "https://github.com/.../bushubot-agent-agent-v0.2-linux-amd64.tar.gz",
  "min_supported_agent_version": "agent-v0.1"
}
```

→ agent 自动更新自己（见 2.3）。

### 2.3 Agent 自我更新

agent 收到 `latest_agent_version != self` 后：

1. 下载新 agent tar.gz
2. 解压
3. 备份当前二进制
4. 覆盖自身二进制（Linux 允许覆盖运行中二进制）
5. 写 `.last-self-update` 标记防循环
6. `os.Exit(0)`
7. systemd 看到进程退出 → `Restart=always` → 用新二进制拉起来

### 2.4 强制升级开关

如果发现 agent-v0.1 有严重 bug：

```
master 控制台执行: /set_min_agent agent-v0.2
```

→ 所有 agent-v0.1 心跳后立刻强制自我更新。

### 2.5 防自我更新死循环

agent 在自己的目录下写 `.last-self-update` 记录刚升的版本。如果再次收到同一个 latest_agent_version，跳过。

防止 master 配置写错（比如把 `latest_agent_version` 写成跟当前 agent 一样的值）导致循环升级。

## 3. 几个常见演化场景

### 场景 A：加一个"按时间窗口更新"功能

**目标：** 客户服务器只在 02:00-04:00 触发更新。

**做法：**
1. master heartbeat 响应加字段 `update_window: "02:00-04:00"`（旧 agent 忽略此字段，行为不变）
2. 新版 agent 解析此字段，在窗口外不触发 `performUpdate`
3. 等所有 agent 都升上来后，老行为自然消失

**不需要：** 改协议、写新 endpoint、强制升级。

### 场景 B：加监控指标

**目标：** agent 上报 CPU/内存/磁盘。

**做法：**
1. 新建 `POST /api/v1/agent/metrics` endpoint（旧 agent 不调用，无影响）
2. 新版 agent 每 5 分钟上报一次
3. master 把数据存到新表 `agent_metrics`

**不需要：** 改 heartbeat。

### 场景 C：彻底重设计 license 校验协议

**目标：** 加密签名授权响应，旧客户跑不了。

**做法：**
1. 新建 `POST /api/v2/agent/heartbeat`
2. 新 agent 走 v2，旧 agent 继续走 v1
3. master 同时维护两个 endpoint
4. 等 90% 客户的 agent 都升级后，下线 v1

**绝不能：** 直接改 v1 协议。

## 4. 兼容性矩阵（你心里要有数）

| master 版本 | agent 版本 | 是否兼容 |
|------------|-----------|----------|
| 任意 | 老版（不报 agent_version） | ✅（master 收到空字段当未上报）|
| 任意 | 新版 | ✅ |
| 老版（不返回 latest_agent_version） | 新版 | ✅（agent 看到空字段不触发自更新） |

## 5. 发布新版 agent 的 SOP

每次想升级 agent：

```bash
# 1. 在 bushubot 仓改 agent 代码 + push tag
git tag agent-v0.2
git push --tags

# 2. CI 自动编译并发到 GitHub Release（agent-release.yml workflow）

# 3. 在主 Bot 里告诉 master 有新版了
/set_agent_version agent-v0.2

# 4. 各客户 agent 下次心跳（≤60 秒）后自动自我更新
```

如果发现 agent-v0.2 有 bug，回滚：

```bash
/set_agent_version agent-v0.1
```

→ 所有 v0.2 的 agent 看到 `latest_agent_version != self` → 自我更新到 v0.1。

⚠️ 但 v0.1 必须仍然存在 GitHub Release 里，所以**不要删旧 release**。

## 6. 发布新版 master 不影响 agent

master 是你完全控制的服务器，随便升级。但发布前确认：

- 新 master 仍然能处理老 agent 的请求（不要删请求字段的处理逻辑）
- 新 master 返回的响应里，没动过老字段的语义
- 数据库迁移是 additive 的（加列、加表），不删

## 7. agent "越傻越好"原则

agent 应该只做最通用的事：

- ✅ 心跳上报
- ✅ 下载文件 + 解压 + 替换二进制
- ✅ systemctl restart
- ✅ 客户 Bot 命令处理（极薄）
- ✅ 自我更新

**不要在 agent 里写：**

- ❌ 业务逻辑（属于 TGfulibot）
- ❌ SQL（agent 不应连客户的数据库）
- ❌ 复杂判断（让 master 来判断）

agent 越傻 → 越少需要升级 → 越少触发"老版 agent 兼容"难题 → 整个系统越稳定。

## 8. 我们做了什么 / 没做什么

**已做（第一版）：**
- agent 上报自己版本
- master 下发最新 agent 版本 + download url + 最低支持版本
- agent 自我更新逻辑
- master 控制台命令 `/set_agent_version` `/set_min_agent` `/version`
- 防自我更新死循环（`.last-self-update` 标记）
- 数据库 settings 表（可扩展任意全局配置）

**未做（明确推后）：**
- 公私钥签名授权响应（MVP 用 HTTPS 即可）
- agent 灰度发布（`/set_agent_version` 是一刀切）
- 协议版本协商（先靠"加字段不删字段"约束，将来需要再加 v2）

只要遵守第 1 节的三条铁律，这套基础设施未来 5 年不被卡住。
