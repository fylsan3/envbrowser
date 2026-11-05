# envbrowser

macOS 上的多环境浏览器管理工具，用于测试自有服务的浏览器指纹识别。

一个环境 = 一个账号 = 一套设备指纹 = 一条代理（socks5h 或 http，均带用户名密码）。

需求与设计见 [`docs/需求规格-v1.html`](docs/需求规格-v1.html)。

## 当前进度：阶段 1（核心层 + 网络积木）

| 模块 | 说明 |
|---|---|
| `internal/config` | `env.json` / `settings.json` 读写，原子写入 |
| `internal/relay` | 本地中转：浏览器以无认证 SOCKS5 连 `127.0.0.1:端口`，中转带认证连上游；上游失败即断，没有直连分支；拒绝本机与内网地址 |
| `internal/egress` | 出口 IP 校验（等于录入值、不等于本机 IP）、1–10 秒随机复查、请求头暴露检查 |
| `internal/launcher` | 环境锁（残留锁自动清除）、Chromium 启动参数、`TZ` |
| `internal/runner` | 串起整个流程：加锁 → 中转 → 出口校验（不通过不启动）→ Chromium → 复查 → 清理 |
| `internal/admin` | 最小管理页，带令牌、Host、Origin 校验（完整管理页在阶段 4） |

## 编译

```bash
# 工具链放在项目目录 .toolchain/（不进仓库），也可用系统 Go 1.22+
export PATH=$PWD/.toolchain/go/bin:$PATH GOTOOLCHAIN=local
go test ./...
go build -o bin/envbrowser ./cmd/envbrowser
```

## 使用

```bash
./bin/envbrowser settings -chromium /path/to/Chromium.app -chromium-version 159.0.8145.0
./bin/envbrowser check  -proxy 'http://用户:密码@主机:端口' -exit 出口IP
./bin/envbrowser create -name 样本A -proxy 'socks5h://用户:密码@主机:端口' -exit 出口IP -tz Asia/Tokyo
./bin/envbrowser list
./bin/envbrowser open env-0001     # 前台运行，Ctrl+C 关闭
./bin/envbrowser serve             # 管理页面 127.0.0.1:8800
```

数据默认存放在 `~/BrowserEnvs`（可用 `ENVBROWSER_ROOT` 修改）。**代理密码和账号密码以明文保存在那里，不在仓库中。**
