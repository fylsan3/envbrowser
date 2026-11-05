# envbrowser（游龙）

macOS 上的多环境浏览器管理工具，用于测试自有服务的浏览器指纹识别。产品名暂定“游龙”，程序名仍为 `envbrowser`。

一个环境 = 一个账号 = 一套设备指纹 = 一条代理（socks5h 或 http，均带用户名密码）。

需求与设计见 [`docs/需求规格-v2.html`](docs/需求规格-v2.html)。

## 浏览器版本（当前唯一支持的版本）

| 项目 | 值 |
|---|---|
| 名称 | fingerprint-chromium（adryfish，BSD-3，基于 Ungoogled Chromium） |
| 版本 | **148.0.7778.215**，macOS arm64（Apple 芯片） |
| 发布页 | https://github.com/adryfish/fingerprint-chromium/releases/tag/148.0.7778.215 |
| 下载地址 | https://github.com/adryfish/fingerprint-chromium/releases/download/148.0.7778.215/ungoogled-chromium_148.0.7778.215-1.1_macos.dmg |
| 大小 | 140,187,500 字节（约 134MB） |
| SHA-256 | `b72f091e2e1a7583eed389c4b8e3534ed355e568af8c8bbf8fc30a25e23ca679` |
| 安装位置 | `.toolchain/fpchromium-148/Chromium.app`（放在这里程序会自动找到） |

一键下载、校验并安装：

```bash
scripts/install-browser.sh           # 校验值不一致会删除安装包并拒绝安装
scripts/install-browser.sh --check   # 只检查已安装的版本
```

## 仓库里有什么、没有什么

仓库里**只放源码、文档和脚本**。以下内容不进仓库：

| 内容 | 原因 | 怎么获得 |
|---|---|---|
| 浏览器 | 第三方软件，解压后约 367MB，超过 GitHub 单文件上限 | `scripts/install-browser.sh` |
| Go 工具链 | 可从官网下载 | 放到 `.toolchain/go/`，或使用系统 Go 1.22+ |
| 编译好的程序 | 由源码生成 | 自己编译（见下方），或从本仓库 Releases 下载 `envbrowser-版本-macos-arm64.zip` |
| 环境数据 `~/BrowserEnvs` | **含明文代理密码** | 程序运行时生成 |

## 模块

| 模块 | 说明 |
|---|---|
| `internal/config` | `env.json`（版本 2）/ `settings.json` 读写；v1 环境自动迁移并留备份；模板轮换、种子各环境不重复 |
| `templates/presets.json` | 内置 4 套 Apple 官网 M2 机型模板（编译时打包进程序） |
| `internal/relay` | 本地中转：浏览器以无认证 SOCKS5 连 `127.0.0.1:端口`，中转带认证连上游；上游失败即断，没有直连分支；拒绝本机与内网地址；端口释放后 60 秒冷却 |
| `internal/egress` | 出口 IP 校验（等于录入值、不等于本机 IP）、1–10 秒随机复查、请求头暴露检查 |
| `internal/launcher` | 环境锁（记录主程序与浏览器 PID）、孤儿浏览器清理、fingerprint-chromium 启动参数 |
| `internal/runner` | 加锁 → 清理孤儿 → 中转 → 出口校验（不通过不启动）→ 浏览器 → 复查 → 清理 |
| `internal/admin` | 最小管理页，带令牌、Host、Origin 校验（完整管理页在阶段 4） |

## 编译

```bash
export PATH=$PWD/.toolchain/go/bin:$PATH GOTOOLCHAIN=local
go test ./cmd/... ./internal/... ./templates/...
go build -o bin/envbrowser ./cmd/envbrowser

# 打包发布（产物在 dist/，只含程序、README、文档、安装脚本）
scripts/release.sh v0.2.0
```

## 使用

```bash
# 浏览器在 .toolchain/fpchromium-148/Chromium.app 时可省略 settings
./bin/envbrowser settings -chromium /path/to/Chromium.app -chromium-version 148.0.7778.215
./bin/envbrowser check  -proxy 'http://用户:密码@主机:端口' -exit 出口IP
./bin/envbrowser create -name 样本A -proxy 'socks5h://用户:密码@主机:端口' -exit 出口IP -tz Asia/Tokyo
./bin/envbrowser list
./bin/envbrowser open env-0001     # 前台运行，Ctrl+C 关闭
./bin/envbrowser serve             # 管理页面 127.0.0.1:8800
```

数据默认存放在 `~/BrowserEnvs`（可用 `ENVBROWSER_ROOT` 修改）。**代理密码和账号密码以明文保存在那里，不在仓库中。**

## 提交前自动检查（铁律：代理 IP、用户名、密码不得进仓库）

克隆后执行一次：

```bash
git config core.hooksPath scripts/hooks
mkdir -p .secrets && chmod 700 .secrets
# 每行一个不许出现的词（真实代理 IP、IP:端口、用户名、密码），此目录已被 .gitignore 忽略
$EDITOR .secrets/forbidden.txt && chmod 600 .secrets/forbidden.txt
```

之后每次 `git commit` 检查暂存内容，`git push` 检查待推送提交和全部历史。命中非文档保留段的公网 IPv4 或清单中的词即拒绝。
