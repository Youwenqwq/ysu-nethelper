# YSU-NetHelper

另一个燕山大学校园网 CLI 认证助手，使用 Go 编写。

## 能做什么

- 查询校园网认证状态
- 登录、登出校园网，支持校园网及运营商网络服务
- 复用当前在线会话免密码切换运营商，提供终端交互菜单
- 以守护进程方式自动检测掉线并重新认证

## 安装

Releases 打包了常用系统架构的软件包，可根据系统直接安装使用；如无对应软件包，可手动下载二进制程序配置使用。

软件包会创建 systemd 配置并放置好配置文件目录。

首次使用先编辑 /etc/ysunethelper/config.json 配置文件，配置好账号密码与服务，然后

```sh
sudo systemctl enable --now ysunethelper.service
```

deploy/ 下也放置了一个 initd 模板，可按需手动配置使用。

## 使用

```sh
ysunethelper [-config path] status [-v] [-test] [--json]
ysunethelper [-config path] login [-u username] [-p password] [-s service] [--json]
ysunethelper [-config path] logout [--json]
ysunethelper [-config path] switch [--json] <service>
ysunethelper [-config path] tui
ysunethelper [-config path] devices [--json]
ysunethelper [-config path] kick [--json] <序号|UUID>...
ysunethelper [-config path] daemon
ysunethelper version
ysunethelper -h
```

全局选项 `-config` 和 `-v` 必须放在命令之前。命令选项放在命令之后、位置参数之前；运行 `ysunethelper <命令> -h` 可查看对应帮助。

### 查询状态

| 命令 | 行为 |
|---|---|
| `ysunethelper status` | 显示姓名、学号、IP、存在时的 MAC、运营商，以及校园网剩余流量 |
| `ysunethelper status -v` | 显示完整状态，包括账户信息和 Portal 返回的全部原始字段 |
| `ysunethelper status --json` | 以 JSON 输出简明状态，供脚本解析（可与 `-v` 组合输出完整字段） |
| `ysunethelper status -test` | 显示简明状态，并执行 Internet 连通性检测 |
| `ysunethelper status -v -test` | 显示完整状态，并执行 Internet 连通性检测 |

剩余流量仅在运营商为“校园网”时显示，并保持上游返回的格式，例如 `28.1GB`。
`status` 默认不会执行 Internet 连通性检测，只有显式指定 `-test` 才会发起探针请求。

默认输出示例：

```text
在线状态: 在线
姓名: 张三
学工号: 202400114514
IP: 10.0.0.2
运营商: 校园网
剩余流量: 28.1GB
```

`--json` 输出示例：

```json
{
  "online": true,
  "name": "张三",
  "username": "202400114514",
  "user_ip": "10.0.0.2",
  "service": "校园网",
  "remaining_traffic": "28.1GB"
}
```

### 登录与登出

登录时不指定账号密码，程序会在需要时从终端交互询问：

```sh
ysunethelper login
```

也可以临时指定账号、密码和网络服务（不会写入配置文件）：

```sh
ysunethelper login -u 202400114514 -p 'your-password' -s campus
```

支持的服务别名：`campus`（校园网）、`unicom`（中国联通）、`telecom`（中国电信）、`mobile`（中国移动）。也可以填写服务全名。
传入 `--json` 时登录结果以 JSON 输出（含 `ok`、`username`、`name`、`service`、`user_ip`、`user_mac` 字段）。

登出：

```sh
ysunethelper logout
```

### 免密码切换运营商

设备已经在线时，使用 `tui` 打开终端菜单，输入编号、中文名称或英文别名切换，`r` 刷新，`q` 退出：

```sh
ysunethelper tui
```

菜单从服务器读取当前会话的完整服务列表，并标记当前运营商。门户可能返回尚未绑定的运营商，账号是否已绑定目标运营商由服务器校验。也可以直接切换：

```sh
ysunethelper switch 3
ysunethelper switch 联通
ysunethelper switch unicom
ysunethelper switch campus
ysunethelper switch --json unicom
```

`switch` 必须提供目标，不带参数只显示用法，不进入交互界面。只有 `tui` 命令打开菜单。支持中文简称、服务全名和英文别名；编号固定，不受门户列表顺序变化影响：

| 编号 | 服务名称 | 英文别名 |
|---|---|---|
| `0` | 校园网 | `campus` |
| `1` | 中国电信（电信） | `telecom` |
| `2` | 中国移动（移动） | `mobile` |
| `3` | 中国联通（联通） | `unicom` |

此功能使用认证网页的 `/eportal/network/setService` 接口和当前在线会话，不需要输入密码，也不依赖本地 CAS 凭据。服务端接受切换后，程序会在最多 20 秒内轮询实际在线状态，等待目标运营商生效；短暂离线、仍显示旧运营商或网络暂时中断时继续等待，不重复提交切换请求。已在目标运营商时不重复切换。开始切换前设备已离线时会提示先 `login`，不会自动询问密码。

切换可能短暂断网；服务端若已下线原连接却未能登录目标服务，会明确报告失败。`switch` 不修改配置，运行中的 daemon 以后重连仍使用其启动时配置的 `service`。需要长期保持新运营商时，应同步修改配置并重启 daemon。`login -s` 的行为保持不变：已经在线时不会切换。

### 在线设备管理

使用当前在线会话免密码查询账号设备，当前设备有标注：

```sh
ysunethelper devices
```

传入 `--json` 输出结构化在线列表（`online`）及接口原始数据（`raw`），供脚本解析：

```sh
ysunethelper devices --json
```

下线指定设备，目标为 `devices` 输出中的序号（1 起）或 UUID，可一次多个：

```sh
ysunethelper kick 2
ysunethelper kick 44484b32a082306001a0a8b4470c6b39
```

`devices` 和 `kick` 均要求本机已在线；不读取账号密码或 CAS 凭据，也不再回退到自助服务登录。离线或会话失效时直接报错。设备来自当前在线账号；下线前会校验所有目标，任何无效目标都会阻止整次提交，重复目标只提交一次。

下线可能有延迟，成功输出表示服务器已接受请求，可用 `devices` 复查。设备序号从 1 开始，与上面的运营商编号不同。

`logout` 和 `kick` 同样支持 `--json`：`logout --json` 输出 `{"ok": true}`；`kick --json` 输出 `ok` 和已提交下线请求的设备结构化列表（`kicked` 数组）。

命令行密码可能被本机其他用户通过进程列表看到，长期使用建议写入权限为 0600 的配置文件。

## 配置

### 配置文件路径

- `daemon`：`-config` 显式指定 → `./ysunethelper.json` → `~/.config/ysunethelper/config.json` → `/etc/ysunethelper/config.json`
- `status`、`login`、`logout`、`switch`、`tui`、`devices`、`kick`：与上述一致，但不检查 `/etc` 下的配置，避免权限问题。

显式指定时，把 `-config` 放在子命令之前：

```sh
ysunethelper -config /path/to/config.json login
```

### 配置内容

系统包提供的配置模板权限为 `0600`。填写统一身份认证账号密码，并按需修改 `service`：

```json
{
  "username": "202400114514",
  "password": "your-password",
  "service": "campus",
  "daemon": {
    "probe_interval": "60s",
    "probe_confirm": 3,
    "probe_confirm_gap": "3s",
    "probe_timeout": "5s",
    "nolink_interval": "15s",
    "backoff_initial": "10s",
    "backoff_max": "10m",
    "no_auth_period": {
      "enabled": false,
      "weekdays": [0, 1, 2, 3, 4],
      "start": "23:30",
      "end": "05:30"
    }
  }
}
```

`service` 支持 `campus`、`unicom`、`telecom`、`mobile` 或服务中文名；省略时默认为校园网。间隔及超时时长使用如 `30s`、`5m` 的格式；禁认证时段使用 `HH:MM`。

| 字段 | 说明 |
|---|---|
| `username` / `password` | daemon 必填，用于 CAS 会话过期后的自动续期。 |
| `service` | 网络服务名，模板默认为 `campus`。 |
| `credential_path` | CAS 会话凭据保存路径，默认 `~/.config/ysunethelper/cas.json`。 |
| `http_timeout` | 认证请求超时时间，默认 `30s`。 |
| `daemon.*` | 自动保活和 Internet 探测参数，模板已提供可用默认值。 |

配置文件存在但未填写账号密码，或 `service` 为空白时，`daemon` 会提示编辑配置并退出，不会开始认证。直接运行不带子命令的 `ysunethelper` 也会提示未完成的系统配置。

完全找不到配置文件时，`daemon` 会在当前目录生成 `ysunethelper.json` 模板，权限为 `0600`。

配置文件包含密码，请勿设置为其他用户可读，也不要提交到公共仓库。

### 禁认证时段（可选）

部分区域在周日～周四晚 23:30 至次日 05:30 不允许认证。默认关闭此策略；需要时将 `daemon.no_auth_period.enabled` 改为 `true`，保存配置并重启 daemon。

| 字段 | 说明 |
|---|---|
| `enabled` | 默认 `false`；省略整个配置块也不会启用。 |
| `weekdays` | 时段**开始日**，`0` 为周日，`1`～`6` 为周一～周六；默认 `[0, 1, 2, 3, 4]`。启用时不能为空。 |
| `start` / `end` | 北京时间，严格使用 `HH:MM`，默认 `23:30` / `05:30`。结束早于开始表示跨日，也支持同日时段；两者不能相同。 |

时段包含开始时刻、不包含结束时刻。例如周四 23:30～周五 05:30 生效，周五和周六晚上不生效，周日凌晨也不生效。

此策略仅影响 `daemon`。

## 构建

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o ysunethelper ./cmd/ysunethelper
go test ./...
```

发布构建通过 `-ldflags "-X main.version=vX.Y.Z"` 注入版本号（见 release workflow）；本地构建的 `ysunethelper version` 显示 `dev`（仓库构建时附带提交号）。

## 项目结构

```text
cmd/ysunethelper/  CLI 入口
internal/config/    配置
internal/authd/     daemon
internal/cas/       CAS
internal/eportal/   ePortal
deploy/             systemd 与 initd 示例
```

## 参考

- [ysu-sdk](https://github.com/Youwenqwq/ysu-sdk)
- [YSUNetLoginv2](https://github.com/KamijoToma/YSUNetLoginv2)

## LICENSE

MIT
