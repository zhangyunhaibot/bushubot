# Cloud-init 自动部署教程

> 你给客户买/创建服务器时，把这套流程跑一遍，**客户拿到的就是已经装好的服务器**，他只需要登录改密码就行。

## 流程总览

```
你: /add 客户名|TG_ID|bot_token
   ↓
master 自动签发 token + 生成 cloud-init 脚本
   ↓
你买云服务器 (阿里云国际版 / 腾讯云 / Vultr 等)
   ↓
在"用户数据"栏粘贴 cloud-init 脚本
   ↓
点"创建" → 等 5 分钟
   ↓
主 Bot 收到 "客户XX 上线" 通知
   ↓
你把服务器 IP + 初始密码 发给客户
   ↓
客户登录改密码 (可选)
```

## 第 1 步：找客户拿信息

私聊客户拿两个东西：

1. **TG user ID** — 让客户给 [@userinfobot](https://t.me/userinfobot) 发任意消息，截图给你
2. **Bot Token** — 让客户去 [@BotFather](https://t.me/BotFather) 创建一个 Bot，复制 token

## 第 2 步：在主 Bot 里 `/add`

```
/add 老王|123456789|7058508504:AAEx...|月费
```

主 Bot 会回复（示例）：

```
✅ 已创建客户 老王（有效期至 2027-05-04）

📦 cloud-init 脚本（在云控制台买服务器时贴到"用户数据"栏）：

#!/bin/bash
curl -fsSL https://install.bushubot.example.com/install.sh | sudo bash -s -- \
  --license 'eyJjdXN0b21lcl9pZCI6Li4u...' \
  --bot-token '7058508504:AAEx...' \
  --admin-id 123456789

授权 token（备查）：
eyJjdXN0b21lcl9pZCI6Li4u...

💡 服务器开机 5 分钟内会自动收到上线通知。
```

**复制 cloud-init 那一段**（含 `#!/bin/bash` 在内）。

## 第 3 步：买服务器（以阿里云国际版轻量为例）

1. 进 [阿里云轻量应用服务器](https://www.alibabacloud.com/product/swas)
2. 选配置：
   - 套餐：2C2G 不限流量 ($5.6/月)
   - 地域：新加坡（或你/客户最近的）
   - 镜像：**Ubuntu 24.04 LTS**（这一步必须 Ubuntu）
3. 找到 **"实例自定义数据"** / **"用户数据"** / **"User Data"** 栏
   - 阿里云轻量：购买页面下方"高级选项" → "实例自定义数据"
   - 阿里云 ECS：实例创建向导第 4 步"系统配置"
   - 腾讯云：购买页面"高级设置" → "启动脚本"
   - Vultr：Deploy 页面 "Server Initialization Script"
4. 把第 2 步复制的 cloud-init 脚本粘贴进去
5. 点"创建"

[推测] 不同云厂商的"用户数据"叫法不同，但都是同一个东西——服务器开机时自动以 root 执行的 shell 脚本。

## 第 4 步：等部署完成

服务器开机后会自动：

1. 装 PostgreSQL / Redis
2. 下载 tgfulibot 主程序
3. 下载 agent
4. 写配置（含 license / bot_token / admin_id）
5. 启动 systemd 服务
6. agent 第一次心跳到 master

时间：[推测] 3-5 分钟（阿里云国内/海外网络差异）。

完成后**主 Bot 自动收到通知**：

```
🟢 客户 老王 上线
   IP: 1.2.3.4
   tgfulibot: v0.13
   agent: agent-v0.1
```

## 第 5 步：把服务器交付给客户

打开云控制台，找到服务器：

- 阿里云轻量：**重置密码** → 设一个临时密码
- 拿到：**IP + root + 临时密码**

发给客户：

```
你的服务器已经装好了，下面是登录信息：

IP:       1.2.3.4
账号:     root
临时密码: TempPass2026!

👉 登录后建议立刻改密码：
   ssh root@1.2.3.4
   passwd
```

客户改不改密码都不影响 tgfulibot 运行——所有授权都跑在 systemd 里。

## 排查：5 分钟后没收到上线通知

### 情况 1: 服务器还在装

```bash
# 你 SSH 进服务器看 cloud-init 日志:
tail -f /var/log/cloud-init-output.log
```

如果还在跑（大量 apt-get / curl 输出），等。
如果停了但没 "部署完成"，看下面情况 2。

### 情况 2: install.sh 报错

```bash
# 看完整 cloud-init 日志:
cat /var/log/cloud-init-output.log

# 常见错误:
#   - apt-get update 失败 → 镜像源问题，换源或重装
#   - 下载 release 失败 → GitHub 不通，检查网络
#   - 缺少 --bot-token 等 → cloud-init 脚本参数没填全
```

### 情况 3: 服务装了但 agent 连不上 master

```bash
# 看 agent 日志:
journalctl -u bushubot-agent -n 50 --no-pager

# 常见错误:
#   - "心跳失败: dial tcp ...: i/o timeout"
#     → master URL 配错 / master 没起 / SSL 证书问题
#   - "invalid license"
#     → token 复制时被截断了，重新 /issue
```

### 情况 4: tgfulibot 启动失败

```bash
journalctl -u tgfulibot -n 80 --no-pager
```

[推测] TGfulibot 接入 license 校验之前，token 不会被检查；接入之后，无效 token 会让 tgfulibot 启动失败。

## 给客户改密码后做的事

客户改密码后**不需要做任何事**。所有服务跑在 root 用户的 systemd 里，与登录 shell 的密码无关。

如果你想给客户更"贴心"的体验，可以：

```
你最后发给客户的话:
"已部署完成，你的 Bot 是 @yourbot，发 /start 试试"
```

客户根本不用知道有"服务器"这个概念。
