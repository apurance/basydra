# Basydra

**One client. Many nodes.** 去中心化 VPS 监控 MVP。

一个 Go 可执行文件，既可运行带内嵌 Web UI 的客户端，也可在 Linux 上运行探针。客户端直接读取各节点，不持久化指标；每个探针独立采集、保存并提供自己的历史。

```text
Browser → local Basydra client → SSH tunnel / HTTPS → Linux probe
                                                   ├─ /proc + statfs
                                                   └─ local JSONL history
```

## 已实现

- 节点添加、删除、搜索、配置持久化；UI 和 CLI 共用客户端 API。
- CPU、内存、Swap、根目录磁盘、网络收发、系统负载、运行时间。
- 默认每 5 秒采样与刷新；24 小时节点本地历史；重启恢复与过期清理。
- 15 分钟 / 1 小时 / 6 小时 / 24 小时历史曲线，查询均匀抽样至最多 2,000 点。
- 在线、连接中、离线状态，保留并标记最后成功读取的数据。
- 自动建立 SSH 隧道、断线重连；或者直接连接 HTTPS 探针。
- Token 鉴权；查询接口不返回 Token；客户端仅监听回环地址。
- Windows、Linux amd64 / arm64 单文件构建，没有第三方 Go / npm 依赖。

## 开发与构建

要求 Go 1.25+，`go` 命令可通过 PATH 找到。无需 Node.js 构建前端。在项目根目录执行：

```powershell
.\scripts\build.ps1
.\bin\basydra.exe serve --config .local/client.json
```

打开 <http://127.0.0.1:17890>。默认配置为当前目录下 `basydra.json`；指定 `.local/client.json` 可避免将真实节点配置混入源码。

构建脚本从 PATH 查找 Go，沿用当前的 Go 环境与缓存配置，并将临时编译文件放在工作区 `.local/tmp`。`bin/` 和 `.local/` 已排除版本控制。交叉编译不需要 MSVC 或 Linux 工具链。

## 在 Linux 上运行探针

生成随机 Token，并保存到权限为 0600 的文件。Token 也可从 `BASYDRA_TOKEN` 环境变量读取，不通过命令行参数传入。

```sh
umask 077
openssl rand -hex 32 > agent.token
./basydra-linux-amd64 agent \
  --listen 127.0.0.1:19790 \
  --token-file agent.token \
  --data-dir ./basydra-data
```

探针约在一个采样周期后就绪。历史保存在 `samples-YYYY-MM-DD.jsonl`；默认保留 24 小时，允许 `--retention 1h` 至 `72h`。磁盘上按 UTC 日清理已完全过期的日志，查询和内存中只保留指定窗口；因此磁盘日志可能额外保留不足一天的旧记录。每次追加执行 fsync。崩溃留下的不完整记录在恢复时跳过。

CPU 使用率根据两次采样的差值计算；内存使用量为 `MemTotal - MemAvailable`；磁盘使用量为根目录 `statfs` 的已用块；网络忽略回环、Docker bridge、`br-*` 与 `veth*`，其余接口累加。复杂桥接/VPN 的流量可能重复计数。历史抽样保留首尾点，曲线不会显示所有短时峰值。

## SSH 部署并添加节点

需要本机 OpenSSH，目标别名能通过 `ssh ai` 免交互连接，主机密钥已信任。自动化命令保持 `StrictHostKeyChecking=yes`。systemd 部署需要目标上的免交互 sudo 和支持 `LoadCredential` 的 systemd。

首次安装可使用部署脚本；它拒绝覆盖已有 Basydra 安装：

```powershell
New-Item -ItemType Directory -Force .local | Out-Null
$tokenBytes = New-Object byte[] 32
[System.Security.Cryptography.RandomNumberGenerator]::Fill($tokenBytes)
[IO.File]::WriteAllText((Join-Path $PWD '.local/ai.token'), [Convert]::ToHexString($tokenBytes).ToLowerInvariant())
.\scripts\deploy-agent.ps1 -Target ai -TokenFile .local/ai.token
```

客户端运行时，在另一终端添加节点；也可使用网页的「添加节点」：

```powershell
.\bin\basydra.exe node add --name ai --ssh ai --url http://127.0.0.1:19790 --token-file .local/ai.token
.\bin\basydra.exe status
.\bin\basydra.exe node remove --id NODE_ID
```

`--url` 是探针在远端机器上的地址。客户端自动选择本机空闲端口并建立 SSH 转发。探针默认无需向公网开放任何端口。

服务命令：

```sh
sudo systemctl status basydra-agent
sudo journalctl -u basydra-agent -n 30
sudo systemctl restart basydra-agent
```

systemd 使用临时服务身份和受限文件系统；凭据源为 `/etc/basydra/agent.token`，通过 `LoadCredential` 提供。可执行文件位于 `/opt/basydra/basydra`，历史目录位于 `/var/lib/basydra`（启用 DynamicUser 时 systemd 会管理其实际位置）。停用测试部署使用 `sudo systemctl disable --now basydra-agent`；删除节点不会自动卸载探针。

## HTTPS 直连

公网监听必须同时提供 TLS 证书与私钥：

```sh
./basydra-linux-amd64 agent --listen 0.0.0.0:19790 \
  --tls-cert server.crt --tls-key server.key \
  --token-file agent.token --data-dir ./basydra-data
```

添加节点时填写 `https://node.example:19790`，不指定 SSH。客户端使用系统 CA 验证证书，不跳过验证，不跟随重定向。直连明文 HTTP 仅接受回环 IP。

## API

| 位置 | 方法和路径 | 用途 |
| --- | --- | --- |
| 客户端 | `GET /api/nodes` | 节点状态与最新指标，不含 Token |
| 客户端 | `POST /api/nodes` | 添加 `{name,url,ssh?,token}` |
| 客户端 | `DELETE /api/nodes/{id}` | 移除配置并关闭连接 |
| 客户端 | `GET /api/nodes/{id}/history?since=RFC3339&limit=360` | 从节点读取历史 |
| 客户端 | `GET /api/health` | 客户端版本 |
| 探针 | `GET /healthz` | 无鉴权就绪检查，不提供资源指标 |
| 探针 | `GET /api/v1/snapshot` | Bearer Token 鉴权的实时快照 |
| 探针 | `GET /api/v1/history?since=RFC3339&limit=360` | Bearer Token 鉴权的历史查询 |

客户端拒绝跨来源请求、非回环 Host 以及不使用 JSON 的写入请求。配置包含探针 Token，请妥善保管；POSIX 下写入权限为 0600，Windows 下继承目录 ACL。SSH 连接沿用本机 SSH 身份与配置。

## 验证

```powershell
.\scripts\build.ps1       # 原生单元测试、go vet、三个目标构建
node --check internal/client/web/app.js
```

```sh
go test -race ./...        # 在带 Go 的 Linux 开发机上执行
```

测试覆盖历史重启恢复/崩溃追加/过期清理、采集解析、鉴权与过期快照、配置持久化与 Token 隐藏、失败保存、离线检测、跨来源请求限制。真实部署验收需检查探针重启后历史连续、停止后客户端离线、恢复后自动上线，以及网页节点管理与曲线。

MVP 暂未实现 Netdata/PCP 适配、告警、多用户、容器级指标、多挂载点、远程进程管理和长期历史压缩。客户端并发请求上限为 8，最多配置 128 个节点。
