# YSU-NetHelper

另一个燕山大学校园网 CLI 认证助手，使用 Go 编写。

## 能做什么

- **CLI**：校园网状态查询、登入、登出
- **Daemon**：校园网登录状态守护进程

## 构建

```sh
# 本机
CGO_ENABLED=0 go build -ldflags='-s -w' -o ysunethelper ./cmd/ysunethelper

# 交叉编译
CGO_ENABLED=0 GOOS=linux GOARCH=arm64  go build -ldflags='-s -w' -o ysunethelper-arm64  ./cmd/ysunethelper
```

`CI` workflow 在推送 `main`/`master` 分支、提交 Pull Request 或手动运行时，会使用
Mihomo 同款的 `MetaCubeX/go` backport 工具链构建 Linux、Darwin、Windows、
FreeBSD、Android 等 90 个平台/架构变体。每个变体会上传一个 Actions Artifact，
其中包含裸二进制及对应的 `.gz`/`.zip`（Linux 发行版还包含 `.deb`、`.rpm` 或
`.pkg.tar.zst`）；另有 `ysunethelper-all-architectures` Artifact，内含全部裸
二进制的总 `tar.gz` 压缩包。

推送 `v*` 标签还会触发 `release.yml`，使用同一套 90 架构构建，将所有架构
二进制、压缩包、发行版包和校验文件发布到 GitHub Release，并附加全架构二进制
压缩包及总校验文件。

## 系统代理

认证请求和 Internet 探针会自动遵循系统代理环境变量：`HTTP_PROXY`、`HTTPS_PROXY` 和 `NO_PROXY`（小写形式同样支持）。例如：

```sh
export HTTPS_PROXY=http://127.0.0.1:7890
export HTTP_PROXY=http://127.0.0.1:7890
ysunethelper status
```

如需让某些地址直连，可设置 `NO_PROXY`，例如 `NO_PROXY=localhost,127.0.0.1`。

## 配置

配置文件解析顺序：`-config` 显式指定 → `./ysunethelper.json` → `~/.config/ysunethelper/config.json` → `/etc/ysunethelper/config.json`

**首次运行**：`daemon` 子命令在找不到配置文件时，会自动在当前目录生成模板 `ysunethelper.json` 并退出，填写 `username`/`password` 后重新运行即可。

```json
{
  "username": "202400114514",
  "password": "your-password",
  "service": "校园网",
  "daemon": {
    "probe_interval": "60s",
    "probe_confirm": 3,
    "probe_confirm_gap": "3s",
    "probe_timeout": "5s",
    "nolink_interval": "15s",
    "backoff_initial": "10s",
    "backoff_max": "10m"
  }
}
```

| 字段 | 默认 | 说明 |
|---|---|---|
| `username` / `password` | — | 统一身份认证账密。Daemon 模式必填（TGC 过期自动续期用） |
| `service` | `校园网` | 网络服务名支持别名 `campus` / `unicom` / `telecom` / `mobile` |
| `credential_path` | `~/.config/ysunethelper/cas.json` | TGC 凭据持久化路径（自动 0600） |
| `http_timeout` | `30s` | 认证类请求超时 |
| `daemon.probe_urls` | 见下 | Internet 204 探针列表 |
| `daemon.probe_interval` | `60s` | 在线时的探测间隔 |
| `daemon.probe_confirm` | `3` | 探针失败需连续确认的次数（防抖） |
| `daemon.probe_timeout` | `5s` | 单次探针超时 |
| `daemon.nolink_interval` | `15s` | portal 不可达（不在校园网）时的重试间隔 |
| `daemon.backoff_initial` / `backoff_max` | `10s` / `10m` | 认证失败指数退避区间 |

默认探针（校园网实测：离线时 NAS 丢包超时，在线时 204）：

```
http://connect.rom.miui.com/generate_204
http://www.gstatic.com/generate_204
http://connectivitycheck.gstatic.com/generate_204
```

## 使用

```sh
ysunethelper [-config path] status
ysunethelper [-config path] login [-u username] [-p password] [-s service]
ysunethelper [-config path] logout
ysunethelper [-config path] daemon
ysunethelper -v ...
```

临时指定账号密码登录（不会写入配置文件）：

```sh
ysunethelper login -u 1145141919810 -p mypassword
```

也可通过 `-s` 指定网络服务，例如 `-s mobile`。命令行密码可能被本机其他
用户通过进程列表看到；长期使用建议写入权限为 0600 的配置文件。

## Daemon 行为

判定链（每轮）：

1. Internet 探针通 → `ONLINE`，按 `probe_interval` 慢速轮询（±20% 抖动）
2. 探针连续失败 `probe_confirm` 次 → 查 portal 状态：
   - portal 不可达 → `NO_LINK`（不在校园网），按 `nolink_interval` 重试，**不**认证
   - portal 说 online 但 Internet 不通（**假死**）→ 先 `offline` 再强制重认证
   - portal 说 offline → 直接重认证
3. 重认证：TGC 有效则直接出票；失效则账密重登 CAS 并持久化新 TGC
4. 认证后验证探针：连续 2 次认证后仍不通 → 判上游故障，进入 `backoff_max` 长退避

错误分类退避：密码错误 / 被要求验证码或 MFA → 长退避并告警（需人工干预）；IP 被冻结 → 长退避；网络错误 → `NO_LINK` 间隔；其他 → 指数退避。

日志为 JSON 行，示例：

```json
{"time":"...","level":"WARN","msg":"检测到假死：portal 在线但 Internet 不通，强制重认证","username":"...","user_ip":"..."}
{"time":"...","level":"INFO","msg":"state transition","from":"OFFLINE","to":"ONLINE"}
```

## 部署

deploy/ 目录下有 initd 或者 systemd 的示例文件可用。

## 项目结构

```
cmd/ysunethelper/      CLI 入口
internal/httpkit/   元数据 cookie 存储、浏览器特征头、手动 302/JS 跳转链
internal/cas/       CAS 网关：账密登录、TGC 检测/持久化、ST 签发
internal/eportal/   ePortal：流程会话、CAS 委托认证、准入、状态、登出
internal/probe/     Internet 204 探针
internal/config/    JSON 配置
internal/authd/     Daemon 状态机与退避编排
```

## 参考

本项目由以下项目提供参考

1. [ysu-sdk](https://github.com/Youwenqwq/ysu-sdk)
2. [YSUNetLoginv2](https://github.com/KamijoToma/YSUNetLoginv2)
## LICENSE

MIT
