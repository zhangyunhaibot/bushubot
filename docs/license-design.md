# License 设计

> 决策记录：为什么用"离线签名 + 可选在线复查"而不是"纯在线心跳"。

## 核心设计

```
master 启动:
  自动生成 RSA 4096 密钥对 → data/keys/master.{key,pub}
  私钥永远不离开 master 服务器

签发 license（你在主 Bot 里执行 /add 或 /issue）:
  master 用私钥签出一个 token (类似 JWT 风格):
    payload: { customer_id, customer_name, issued_at, expires_at, license_id }
    signature: RSA-SHA256(payload, private_key)

客户安装 (install.sh 把 token 写入 config.json):
  TGfulibot 启动 →
    用编译时内嵌的 master 公钥验证 token 签名 →
    检查 expires_at →
    通过即启动业务 (整个过程完全离线，不联网)

agent 心跳:
  仅用于:
    1. 上报客户状态 (在线、版本)
    2. 触发自动更新
    3. 接收远程吊销信号 (enabled=false)
  master 挂了不影响 tgfulibot 主程序运行
```

## 三种威胁如何应对

| 威胁 | 防御机制 | 效果 |
|------|----------|------|
| 别人偷公开仓库的二进制跑 | 离线签名校验 | ❌ 跑不起来（没你签发的 token） |
| 客户跑路想关停 | agent + master enabled 远程吊销 | ✅ 你 `/disable` → 客户 stop |
| Master 短期挂掉 | 离线签名 + agent 宽限期 | ✅ 客户继续跑，超过 7 天才停 |
| Master 永久挂掉 | License 365 天有效期 | ⚠️ 客户跑到 license 过期为止 |
| 客户拔网线避吊销 | agent 宽限期 7 天后 stop | ⚠️ 撑 7 天后停服 |
| Token 被篡改 | RSA 签名 | ❌ 签名验不过 |
| License 365 天到期 | TGfulibot 启动校验 + 12 小时复查 | ⚠️ 强制重新签发 |

## 关键文件

```
master/
  internal/license/
    token.go          ← Token 编解码 + Sign + Verify（核心）
    keypair.go        ← RSA 密钥对生成、加载、PEM 导出
    token_test.go     ← 单元测试（4 个 case 全过）
  data/keys/          ← 密钥存储（gitignored，运行时生成）
    master.key        ← 私钥（权限 600）
    master.pub        ← 公钥（拷贝到 TGfulibot 项目）

agent/
  internal/config/    ← 加 grace_days_offline 字段
  cmd/main.go         ← 心跳失败不立即 stop，超过宽限才 stop

tgfulibot-integration/
  verify.go           ← 给 TGfulibot 拷贝的离线校验代码
  README.md           ← 接入指南
  main_snippet.go.txt ← main.go 改动示例
```

## License 流转

```
1. master 启动
   ├─ 检查 data/keys/master.key
   ├─ 不存在则生成 4096 位 RSA
   └─ 加载到内存

2. 你: /add 老王|TG_ID|bot_token
   ├─ store.CreateCustomer → customers 表新增记录
   ├─ license.Sign(payload, priv) → 出 token
   └─ 主 Bot 私信你（你再发给客户）:
        授权 token + 安装命令

3. 客户: curl install.sh | sudo bash -s -- '<token>'
   ├─ install.sh 写 config.json:
   │     license.token = "<token>"
   ├─ 装 tgfulibot + agent
   └─ 启动 tgfulibot.service:
        license.Verify(token, embeddedPub) → 通过 → 业务上线

4. 日常运行:
   ├─ tgfulibot: 每 12h 复查 token（防到期边界）
   ├─ agent:     每 60s 心跳到 master
   │     ├─ 心跳成功 + enabled=true → 没事
   │     ├─ 心跳成功 + enabled=false → systemctl stop tgfulibot
   │     └─ 心跳失败 → 累计离线时长，超过 grace_days 才 stop
   └─ master 挂了 → tgfulibot 继续跑（离线校验）

5. License 到期 / 续期:
   ├─ 临近到期，你: /issue 老王 365 → 出新 token
   ├─ 客户更新 config.json
   └─ systemctl restart tgfulibot
```

## 跟 master heartbeat auth 的关系

之前 agent 的 heartbeat 用一个 short license_key (32 字节随机串) 做 Bearer auth。
**现在统一了**：agent 直接用 license token 做 Bearer auth，master 端：

```go
func Auth() {
    payload := license.Verify(bearerToken, masterPub)
    customer := store.FindByID(payload.CustomerID)
    // ...
}
```

好处：
- 一个客户只需要一个东西（token）
- token 篡改 → master 立刻拒绝（签名验不过）
- token 过期 → master 也拒绝（自带过期检查）

## 不做的事（明确）

- ❌ Token 绑定服务器机器码（朋友圈分销不需要这么严）
- ❌ 公私钥轮换机制（要换公钥就重新发布 TGfulibot 新版本）
- ❌ 多客户共享 license（一码一客户）
- ❌ 公钥从 master 在线下发（编译时内嵌更安全）

## 公钥怎么给 TGfulibot

**两种途径：**

1. **主 Bot 命令**：`/pubkey` → 主 Bot 把 PEM 发给你 → 复制粘贴到 TGfulibot 源码
2. **服务器文件**：`cat /opt/bushubot-master/data/keys/master.pub`

把内容保存到 TGfulibot 项目：

```
TGfulibot/backend/internal/license/master_public_key.pem
```

verify.go 用 `//go:embed` 编译时打入。

## 公钥泄露怎么办

**不重要**。公钥本来就是公开的，谁拿到都没法签发 token。
只要私钥（master.key）不外泄，整个体系就是安全的。

## 私钥保护

master.key **必须**：
- 权限 600
- 属主只能是 master 进程的运行用户
- 不要进 git
- 不要传到任何客户服务器
- master 服务器要做基础硬化（防 SSH 弱密码、防公网 PG）
- 定期 ✅ 备份到你能控制的地方（不丢就行，加密备份更稳）

如果私钥泄露：
- 立刻生成新密钥对
- 把新公钥编进 TGfulibot 新版本
- 让所有客户升级 TGfulibot（agent 自动更新）
- 用新私钥重新给所有客户签 token
- 推送新 token 给客户更新 config.json

## 当前实现状态

✅ 已完成：
- license/ 包（Sign / Verify / 密钥管理）
- master 启动时自动生成密钥
- 主 Bot 命令：`/add`（自动签发）、`/issue`（重签）、`/pubkey`（导出公钥）
- master heartbeat auth 改用 token 验签
- agent 宽限期机制
- 单元测试 4 个 case

⏸ 留给 TGfulibot 主项目接入：
- 拷贝 verify.go + master_public_key.pem
- main.go 加 license.Verify 调用
- config.json 加 license.token 字段

⏸ 后期可加：
- 机器绑定字段（Payload 里加 machine_id）
- License 历史/吊销列表表
- Token 兑换记录（用于追踪谁泄露 token）
