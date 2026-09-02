# YSU-NetHelper

另一个燕山大学校园网 CLI 认证助手，使用 Go 编写。

## 能做什么

- 查询校园网认证状态
- 登录、登出校园网，支持校园网及运营商网络服务
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
ysunethelper [-config path] status
ysunethelper [-config path] login [-u username] [-p password] [-s service]
ysunethelper [-config path] daemon
ysunethelper -v ...
```

查询状态：

```sh
ysunethelper status
```

登录时不指定账号密码，程序会在需要时从终端交互询问：

```sh
ysunethelper login
```

也可以临时指定账号、密码和网络服务：

```sh
ysunethelper login -u 202400114514 -p 'your-password' -s campus
```

支持的服务别名：`campus`（校园网）、`unicom`（中国联通）、`telecom`（中国电信）、`mobile`（中国移动）。也可以填写服务全名。

登出：

```sh
ysunethelper logout
```

命令行密码可能被本机其他用户通过进程列表看到，长期使用建议写入配置文件。

## 配置

### 配置文件路径

- `daemon`：`-config` 显式指定 → `./ysunethelper.json` → `~/.config/ysunethelper/config.json` → `/etc/ysunethelper/config.json`
- `status`、`login`、`logout`：与上述一致，但不检查 `/etc` 下的配置，避免权限问题。

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
    "backoff_max": "10m"
  }
}
```

`service` 支持 `campus`、`unicom`、`telecom`、`mobile` 或服务中文名；省略时默认为校园网。时间值使用如 `30s`、`5m` 的格式。

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

## 构建

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o ysunethelper ./cmd/ysunethelper
go test ./...
```

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
