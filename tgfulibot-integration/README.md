# TGfulibot 接入 bushubot license 校验

> 这个目录是给 TGfulibot 主项目"接入"用的代码模板。
> bushubot 项目本身不依赖这里的文件，**只是给后续接入提供拷贝源**。

## 接入只需 3 步

### 1. 拿到 master 公钥

在主 Bot 里执行：

```
/pubkey
```

复制 `-----BEGIN PUBLIC KEY-----` 到 `-----END PUBLIC KEY-----` 之间的全部内容。

### 2. 拷贝到 TGfulibot 项目

```
TGfulibot/backend/internal/license/
├── verify.go                ← 拷贝本目录的 verify.go
└── master_public_key.pem    ← 把第 1 步拿到的公钥保存到这里
```

⚠️ 注意：拷贝 `verify.go` 后，把文件顶部的 `package license` 保留即可，
其他内容**不要改**——这样将来 bushubot 升级了 token 协议，你重新拷一次就能升级。

### 3. 在 main.go 启动时校验

照 `main_snippet.go.txt` 改 `backend/cmd/main.go`，关键三段：

```go
// (1) 加 import
import "telegrambot/internal/license"

// (2) 启动时校验，失败直接 fatal
licPayload, err := license.Verify(cfg.License.Token)
if err != nil {
    log.Fatalf("license 校验失败: %v", err)
}

// (3) 后台 goroutine 每 12 小时复查一次（应对到期边界）
go func() {
    for range time.Tick(12 * time.Hour) {
        if _, err := license.Verify(cfg.License.Token); err != nil {
            log.Printf("license 复查失败: %v，退出", err)
            os.Exit(2)
        }
    }
}()
```

### 4. config 加字段

`backend/internal/config/config.go` 里 Config 结构体加：

```go
type LicenseConfig struct {
    Token string `json:"token"`
}
type Config struct {
    // ... 已有字段
    License LicenseConfig `json:"license"`
}
```

`config.example.json` 加：

```json
{
  ...
  "license": {
    "token": "PASTE_LICENSE_TOKEN_FROM_MASTER_HERE"
  }
}
```

## 完整流程示例

```
你（主 Bot）:
  /add 老王|123456789|<bot_token>|备注
  → master 自动签发 365 天 token
  → 私信给客户:
    授权 token: eyJjdXN0b21lcl9pZCI6...
    安装命令:   curl -fsSL https://install.../install.sh | sudo bash -s -- 'eyJj...'

客户:
  在自己服务器跑安装命令
  install.sh 把 token 写进 /opt/tgfulibot/backend/config.json
  → tgfulibot 启动 → license.Verify(token) → 通过 → 业务上线

365 天后:
  license.Verify 返回 ErrExpired
  → tgfulibot 启动失败 / 复查 goroutine os.Exit(2)
  → systemd 反复重启都失败
  → 客户找你续期
  → 你 /issue 老王 365 → 给新 token → 客户更新 config.json → 重启
```

## 安全保证

| 攻击场景 | 是否能成功 |
|---------|-----------|
| 别人下载你公开的 tgfulibot 二进制直接跑 | ❌ 没有 token，启动失败 |
| 别人伪造一个 license token | ❌ 没有 master 私钥，签名验不过 |
| 客户复制 token 给朋友用 | ⚠️ 能用（token 不绑定服务器，MVP 不做） |
| 客户篡改 token 改到期时间 | ❌ 改了签名就对不上 |
| Master 服务器挂了 | ✅ 客户继续跑（离线校验，不依赖 master） |
| 客户 license 到期 | ❌ 启动失败，要找你续期 |

## 不在这里做的事

- **远程吊销**：在 agent 里做（agent 收到 master 的 enabled=false 会 stop tgfulibot）。
- **机器绑定**：MVP 不做。token 不绑定 IP / hostname / 机器码。
  - 如果将来要做，在 Payload 里加 `machine_id` 字段，verify 时本地比对即可。
- **签名公钥轮换**：MVP 不做。要换公钥需要重新发布 TGfulibot 新版本。

## 协议演化纪律

照 `bushubot/docs/protocol-evolution.md` 的"只增不删"原则。token Payload 加新字段时，老版本 TGfulibot 用 json.Unmarshal 自动忽略，不会挂。
