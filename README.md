# SpeedTaier CLI

基于「全球网测」测速节点的命令行测速工具：选定省份后探测该省全部节点的 IPv4/IPv6 连通性，再为每个运营商选择时延最低的节点测速，输出下载、上传、空载时延、抖动、负载时延等指标，支持 JSON 输出。

> 本项目为个人开发的非官方工具，与全球网测及其运营方无关。工具调用的是全球网测客户端使用的公开接口，接口变化可能导致工具失效。

## 特性

- **按省份测速**：列出省内全部城市、运营商的节点及 IPv4/IPv6 连通性，每个运营商自动选时延最低的节点，失败时换下一个。
- **准确的测速方法**：预热期数据不计入结果；吞吐收敛后提前结束；多连接错峰启动、断流重连。详见[测速方法](#测速方法)。
- **时延更真实**：用长连接上的应用层往返测时延，并在传输期间同时测量负载时延(反映缓冲膨胀)。
- **识别透明代理**：检测到 Surge、Clash、sing-box 等 TUN 模式时，测速流量自动改由物理网卡直接发出，避免结果失真。
- **IPv4 / IPv6 双栈**：本机有 IPv6 路由时自动加测 IPv6。
- **适合脚本**：`--json` 输出稳定结构的单行 JSON，带警告编码与退出码。
- **单一静态二进制**：支持 macOS、Linux、Windows，以及 OpenWrt/iStoreOS 等[软路由](#软路由)(x86、ARM、MIPS)，提供一键安装脚本。

## 安装

### 一键安装

适用于 macOS、Linux 以及 OpenWrt/iStoreOS 等软路由：

```bash
curl -fsSL https://raw.githubusercontent.com/cosxs/speedtaier-cli/main/scripts/install.sh | sh
```

没有 curl 的系统(如 OpenWrt)使用 wget：

```bash
wget -qO- https://raw.githubusercontent.com/cosxs/speedtaier-cli/main/scripts/install.sh | sh
```

脚本兼容 POSIX sh(包括 BusyBox ash)，会自动识别系统与架构，下载最新版并校验 sha256 后安装：root 安装到 `/usr/local/bin` 或 `/usr/bin`，普通用户安装到 `~/.local/bin`。再次运行即升级。可通过环境变量调整(放在 `sh` 前，如 `... | VERSION=v0.6.2 sh`)：

| 变量 | 说明 |
|---|---|
| `VERSION` | 安装指定版本，如 `v0.6.2`；默认最新版 |
| `INSTALL_DIR` | 安装目录 |
| `GITHUB_PROXY` | GitHub 下载加速前缀，最终地址为 前缀 + 原始地址 |
| `ARCH` | 跳过自动检测，指定 `amd64`、`arm64`、`armv7`、`386`、`mips`、`mipsle` |

访问 GitHub 困难时，可以给脚本地址和下载地址都加上 GitHub 加速服务的前缀：

```bash
curl -fsSL https://加速服务地址/https://raw.githubusercontent.com/cosxs/speedtaier-cli/main/scripts/install.sh | GITHUB_PROXY=https://加速服务地址/ sh
```

加速服务能看到并修改经过它的全部内容(包括校验文件)，请只使用可信的服务。

卸载：

```bash
rm "$(command -v speedtaier)"
```

### 其他方式

**Windows**：从 [Releases](https://github.com/cosxs/speedtaier-cli/releases/latest) 下载 `speedtaier-windows-amd64.zip`，解压后运行 `speedtaier.exe`。

**手动下载**：从 Releases 下载对应平台的压缩包，解压后将 `speedtaier` 放入 `PATH`。可用 `checksums-sha256.txt` 校验：

```bash
shasum -a 256 -c checksums-sha256.txt --ignore-missing
```

**使用 go install**(需要 Go 1.26+)：

```bash
go install github.com/cosxs/speedtaier-cli/cmd/speedtaier@latest
```

go install 使用本机的 Go 版本编译；预编译版本固定使用已修复已知漏洞的 Go 版本构建，建议优先使用。

**从源码构建**：

```bash
git clone https://github.com/cosxs/speedtaier-cli.git && cd speedtaier-cli
go build -o speedtaier ./cmd/speedtaier
```

## 快速开始

```bash
speedtaier                                  # 交互选择省份(默认 IP 归属地)并测速
speedtaier --province jiangsu               # 指定省份(拼音)
speedtaier --province jiangsu --family 4    # 只测 IPv4
speedtaier list --province guangdong        # 只查看节点及连通性，不测速
speedtaier --province jiangsu --json        # 输出 JSON
```

省份使用拼音，不区分大小写。注意 **陕西为 `shaanxi`**、山西为 `shanxi`，内蒙古为 `neimenggu`，西藏为 `xizang`。启用 shell 补全后可补全省份：

```bash
speedtaier completion zsh > "${fpath[1]}/_speedtaier"
```

## 输出说明

测速前会列出省内全部节点：

```
城市 │ 运营商 │ 节点     │ IPv4    │ IPv6
南京 │ 电信   │ 南京电信 │ 26.6 ms │ 未探测
南通 │ 移动   │ 南通移动 │ 31.9 ms │ -
```

- `xx ms`：应用层往返时延；`不可达`：有地址但无法完成测速协议的请求；`-`：节点没有该协议的地址；`未探测`：本机没有该协议的路由。

测速结果：

| 列 | 含义 |
|---|---|
| 下载 / 上传 | 预热结束后的平均吞吐 |
| 空载时延 | 测速前空闲状态下往返时延的中位数 |
| 抖动 | 按时间顺序相邻两次往返时延之差的平均 |
| 失败率 | 时延请求失败(超时、断开)的比例，**不等同于丢包率** |
| 负载时延 ↓/↑ | 下载、上传进行时往返时延的中位数，明显高于空载时延说明链路存在缓冲膨胀 |

## 测速方法

1. **选节点**：并发探测省内全部节点，按运营商分组，各组按时延从低到高依次尝试，首个测速成功的节点即为该运营商的结果。
2. **空载时延**：在一条长连接上连续请求，丢弃首次(含建连)，统计其余样本。
3. **预热**：下载前 1.5 秒、上传前 3 秒的数据不计入结果，以排除 TCP 慢启动与发送缓冲区填充的影响。
4. **收敛判定**：预热后每秒统计一次窗口吞吐，连续 3 个窗口都在其均值 ±5% 以内即提前结束；每阶段最短 3 秒、最长 `--seconds` 秒(默认 10)。
5. **负载时延**：传输期间另开一条连接，每 100 毫秒测量一次往返时延。
6. **多连接**：默认 8 条连接错峰 200 毫秒启动，单条断开后自动重连；结束时以 RST 关闭连接，避免残余数据在测速后继续占用带宽。

一次完整测速(三个运营商)约 1 分钟；在千兆宽带上约消耗 1–2 GB 流量。

## 透明代理

Surge 增强模式、Clash/sing-box TUN 等透明代理会接管默认路由：TCP 握手在本地完成(任何地址都会在 1 毫秒内"连上")，上传数据被代理大量缓冲后慢慢发出。经代理测速时，时延与上传结果会严重失真，前一个节点积压的上传数据还会拖累后续节点。

默认的 `--interface auto` 会检测这种情况，并把测速连接绑定到主物理网卡(如 `en0`)直接发出，同时给出提示。也可以手动控制：

```bash
speedtaier --interface en0     # 指定网卡
speedtaier --interface none    # 按系统路由，即经过代理测速
```

绑定网卡在 macOS 与 Linux 上可用(Linux 较旧的内核需要 `CAP_NET_RAW` 权限)；其他平台绑定失败时会给出警告并退回按系统路由测速。

## 软路由

在路由器上直接测速，测的是 WAN 口线路本身，排除了 Wi-Fi、内网与电脑性能的影响。

| 设备 | 架构 | 发布包 |
|---|---|---|
| x86 软路由(J4125、N100 等) | x86_64 | `linux-amd64` |
| R2S/R4S/R5S、RK3568/RK3588、N1 等 | ARM64 | `linux-arm64` |
| 32 位 ARM 设备 | ARMv7 | `linux-armv7` |
| MT7621 等刷 OpenWrt 的硬路由 | MIPS 小端 | `linux-mipsle` |
| 部分高通 ath79 等设备 | MIPS 大端 | `linux-mips` |

一键安装脚本会自动选择合适的包(MIPS 的大小端从 `/etc/openwrt_release` 或系统程序的文件头判断)。程序为静态编译，不依赖系统 C 库，测速时内存占用约 30 MB。

注意事项：

- **存储空间**：解压后约 9–11 MB。闪存较小(如 16 MB)的设备可以安装到内存盘，重启后需要重新安装：
  ```bash
  wget -qO- https://raw.githubusercontent.com/cosxs/speedtaier-cli/main/scripts/install.sh | INSTALL_DIR=/tmp sh
  ```
- **CPU 可能成为瓶颈**：性能较弱的设备(MIPS、Cortex-A53 级 ARM)可能跑不满千兆。测速时可用 `top` 观察，CPU 占满说明结果受限于路由器本身；MIPS 设备的结果只适合作参考。
- **PPPoE 拨号与代理插件**：PPPoE 的 WAN 口(如 `pppoe-wan`)不会被误判为代理；OpenClash 等插件的 TUN 模式会被识别，测速连接自动绑定 WAN 口绕开代理。只代理局域网设备流量的 iptables/nftables 透明代理不影响路由器自身的测速。
- **定时测速**：交互选择需要终端，定时任务中请指定省份，结果按行追加为 JSON。每次测速约消耗 1–2 GB 流量，请控制频率。以 OpenWrt 为例，执行 `crontab -e` 添加：
  ```
  0 4 * * * /usr/bin/speedtaier --province jiangsu --json --non-interactive >> /root/speedtaier.jsonl 2>/dev/null
  ```

## JSON 输出

`--json` 向 stdout 输出单行 JSON(便于按行追加到日志文件)，stderr 无输出，不交互。示例(节选并格式化)：

```json
{
  "schema_version": 1,
  "version": "v0.6.0",
  "started_at": "2026-10-14T15:10:10.032+08:00",
  "duration_ms": 36757,
  "config": { "province": "shandong", "family": "auto", "interface": "auto", "threads": 8, "max_seconds": 3, "...": "..." },
  "client": { "ip": "203.0.113.5", "country": "中国", "province": "山东", "city": "济南", "operator": "联通" },
  "province": "山东",
  "network": { "interface": "en0", "transparent_proxy": "utun4", "proxy_bypassed": true, "ipv6": false },
  "candidates": [
    {
      "id": "1002", "name": "济南电信", "city": "济南", "operator": "电信",
      "ipv4": { "address": "58.56.118.126:65499", "status": "ok", "rtt_ms": 22.97 },
      "ipv6": { "address": "[240e:4e:4000::3]:65499", "status": "skipped" }
    }
  ],
  "tests": [
    {
      "operator": "电信", "family": "ipv4", "status": "ok",
      "node": { "id": "2257001", "name": "东营电信2", "city": "东营", "operator": "电信", "address": "58.58.160.78:65499" },
      "idle_latency": { "samples": 8, "failed": 0, "fail_rate_pct": 0, "min_ms": 13.31, "median_ms": 15.44, "max_ms": 16.93, "jitter_ms": 1.73 },
      "download": { "mbps": 174.41, "bytes": 82294560, "duration_ms": 3000, "stable": false, "loaded_latency": { "median_ms": 163.76, "...": "..." } },
      "upload": { "mbps": 33.88, "bytes": 53683260, "duration_ms": 3249, "stable": false, "loaded_latency": { "median_ms": 87.32, "...": "..." } }
    }
  ],
  "data_usage_bytes": 415702436,
  "warnings": [
    { "code": "proxy_bypassed", "level": "info", "message": "检测到透明代理(utun4)，测速流量改由物理网卡 en0 直接发出(--interface none 可关闭)" }
  ],
  "degraded": false,
  "exit_code": 0
}
```

主要字段：

| 字段 | 说明 |
|---|---|
| `candidates[].ipv4/ipv6.status` | `ok`、`unreachable`、`skipped`(本机无该协议路由)、`no_address`(节点无该协议地址) |
| `tests[].status` | `ok` 或 `failed`；失败时 `error` 给出原因 |
| `tests[].attempts` | 该运营商此前测速失败的节点及原因 |
| `download/upload.stable` | 吞吐收敛后提前结束为 `true`，跑满最长时长为 `false` |
| `data_usage_bytes` | 本次运行消耗的流量(含失败重试与预热) |
| `degraded` | 有测速失败或存在 `warn` 级别警告时为 `true` |

`warnings[].code` 取值：

| code | 级别 | 含义 |
|---|---|---|
| `proxy_bypassed` | info | 检测到透明代理，已绑定物理网卡绕开 |
| `transparent_proxy` | warn | 测速经过透明代理，时延与上传结果可能失真 |
| `province_from_geo` | info | 未指定省份，按归属地选择 |
| `node_fallback` | info | 节点测速失败，已换下一节点 |
| `ipv6_lookup_failed` | warn | 部分运营商的 IPv6 节点获取失败 |
| `interrupted` | warn | 用户中断 |

`schema_version` 在字段含义发生不兼容变化时递增。

## 退出码

| 退出码 | 含义 |
|---|---|
| 0 | 全部成功 |
| 1 | 启动失败或参数错误 |
| 2 | 部分测试失败，或存在 `warn` 级别警告 |
| 130 | 用户中断 |

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--province` | 交互选择 | 省份拼音；非交互环境未指定时使用 IP 归属地 |
| `--family` | `auto` | `auto`(本机有 IPv6 时加测 IPv6)、`4`、`6` |
| `--interface` | `auto` | `auto`、`none` 或网卡名，见[透明代理](#透明代理) |
| `--json` | | 输出 JSON |
| `--non-interactive` | | 不交互选择省份 |
| `--seconds` | `10` | 每阶段最长计量秒数(不含预热)，吞吐稳定后提前结束 |
| `--threads` | `8` | 并发连接数 |
| `--latency-count` | `8` | 空载时延采样次数 |
| `--timeout` | `0` | 单节点总超时秒，0 表示按测速时长自动计算 |
| `--no-download` / `--no-upload` | | 跳过下载 / 上传 |
| `--no-progress` | | 关闭实时进度 |

设置环境变量 `NO_COLOR` 可关闭彩色输出。

## 网络访问

工具会访问以下地址：

| 地址 | 用途 |
|---|---|
| `dlcv2.cnspeedtest.cn:8443` | 配置清单、IP 归属地、IPv6 节点匹配 |
| `down.cnspeedtest.cn:8043`(以配置清单为准) | 加密的全量节点库 |
| 各测速节点 TCP `65499`、`9443` 端口 | 时延探测、鉴权、下载、上传 |
| `raw.githubusercontent.com`、`github.com` | 仅一键安装脚本使用：下载脚本与发布包 |

检测 IPv6 路由与透明代理时只做本机路由查找，不发送数据。节点库缓存在系统缓存目录下的 `speedtaier/` 中(macOS 为 `~/Library/Caches`，Linux 为 `~/.cache`)，按配置清单的 MD5 校验，内容未变化时不重复下载；卸载时可一并删除。

## 开发

需要 Go 1.26+；项目通过 `go.mod` 的 `toolchain` 指令固定构建工具链版本，开发工具(staticcheck、govulncheck)通过 `tool` 指令固定，无需全局安装。

```bash
scripts/check.sh            # gofmt、vet、go fix、staticcheck、shellcheck、测试(含竞态检测)、govulncheck、交叉编译、冒烟测试
scripts/check.sh --quick    # 跳过竞态检测、漏洞扫描与交叉编译
VERSION=v0.6.0 scripts/build.sh   # 构建多平台发布包到 dist/
go test -run='^$' -fuzz='^FuzzParseNodes$' -fuzztime=30s ./internal/taier   # 模糊测试
```

推送 `v*` 标签时，GitHub Actions 会运行检查、构建并创建 Release。

```
cmd/speedtaier/  程序入口
internal/
  cli/       命令行参数、终端界面、JSON 输出
  runner/    编排：选省份 → 选出口网卡 → 探测 → 按运营商测速；产出 Result
  taier/     全球网测协议与测速引擎(计量、时延、探测)
  netx/      平台相关网络能力：绑定网卡、查路由表、检测透明代理
  fakenode/  模拟测速节点，供测试使用
```

## 许可证

[MIT](LICENSE)
