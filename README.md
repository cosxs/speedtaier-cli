# SpeedTaier CLI

基于「全球网测」测速节点的命令行测速工具：按 IP 归属地自动选择本省、本运营商时延最低的节点(本市优先)测速，输出下载、上传、空载时延、抖动、负载时延等指标。

> 本项目为个人开发的非官方工具，与全球网测及其运营方无关。工具调用的是全球网测客户端使用的公开接口，接口变化可能导致工具失效。

## 特性

- **自动选节点**：按 IP 归属地确定省份、运营商与所在城市，无需手动选择；优先测本市时延最低的节点，本市没有再测本省其他城市，失败时换下一个。也可以指定省份、运营商，或为每个运营商各测一个节点。
- **单线程与多线程**：下载、上传各测一次单连接和一次多连接，分别反映单条 TCP 连接的表现与线路的总带宽。
- **准确的测速方法**：预热期数据不计入结果；吞吐收敛后提前结束；多连接错峰启动、断流重连。详见[测速方法](#测速方法)。
- **时延更真实**：用长连接上的应用层往返测时延，并在传输期间同时测量负载时延(反映缓冲膨胀)。
- **识别透明代理**：检测到 Surge、Clash、sing-box 等 TUN 模式时，测速流量自动改由物理网卡直接发出，避免结果失真。
- **IPv4 / IPv6 双栈**：本机有 IPv6 路由时自动加测 IPv6。
- **适合定时任务**：输出不是终端时自动关闭颜色与实时刷新，得到纯文本，并带退出码。
- **单一静态二进制**：支持 macOS、Linux、Windows，以及 OpenWrt/iStoreOS 等[软路由](#软路由)(x86、ARM)，提供一键安装脚本。

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
| `ARCH` | 跳过自动检测，指定 `amd64`、`arm64`、`armv7`、`386` |

访问 GitHub 困难时，可以给脚本地址和下载地址都加上 GitHub 加速服务的前缀：

```bash
curl -fsSL https://加速服务地址/https://raw.githubusercontent.com/cosxs/speedtaier-cli/main/scripts/install.sh | GITHUB_PROXY=https://加速服务地址/ sh
```

加速服务能看到并修改经过它的全部内容(包括校验文件)，请只使用可信的服务。

卸载：

```bash
rm "$(command -v speedtaier)"
```

### 升级

```bash
speedtaier upgrade           # 升级到最新版本
speedtaier upgrade --check   # 只检查是否有新版本
```

`upgrade` 从 GitHub Releases 下载与当前平台匹配的发布包，校验 sha256、确认新程序能运行后才替换当前程序，任何一步失败都会保留原程序。没有程序所在目录的写权限时(如装在 `/usr/local/bin`)请用 `sudo speedtaier upgrade`。重新运行一键安装脚本同样可以升级。

在终端里运行测速时，工具会在后台检查新版本(24 小时内最多联网一次，结果缓存在系统缓存目录；检查失败或超时都不会影响测速)，有新版本时在结束后提示：

```
  INFO    发现新版本 v0.7.0(当前 v0.6.2)，运行 speedtaier upgrade 升级
```

下列情况不检查：stderr 不是终端(定时任务、管道)、开发构建。也可以用 `--no-update-check` 或环境变量 `SPEEDTAIER_NO_UPDATE_CHECK=1` 关闭。工具不会自动替换自身，升级只在你运行 `upgrade` 时发生。

访问 GitHub 困难时，设置 `GITHUB_PROXY`(用法同一键安装脚本)让检查与升级走加速地址：

```bash
GITHUB_PROXY=https://加速服务地址/ speedtaier upgrade
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
speedtaier                                  # 按 IP 归属地自动选节点测速
speedtaier --province jiangsu               # 指定省份(拼音)，不按归属地
speedtaier --province jiangsu --family 4    # 只测 IPv4
speedtaier --no-single                      # 只测多线程，流量约减半
speedtaier --threads 1                      # 只测单线程
speedtaier --operator all                   # 电信、联通、移动等全部运营商各测一个节点
speedtaier --operator dianxin               # 指定运营商(中文、拼音或英文)
speedtaier list --province guangdong        # 只查看节点及连通性，不测速
speedtaier upgrade                          # 升级到最新版本
```

省份使用拼音，不区分大小写。注意 **陕西为 `shaanxi`**、山西为 `shanxi`，内蒙古为 `neimenggu`，西藏为 `xizang`。启用 shell 补全后可补全省份：

```bash
speedtaier completion zsh > "${fpath[1]}/_speedtaier"
```

## 输出说明

测速按阶段逐个展示：先是空闲时延，然后是下载(单线程、多线程)、上传(单线程、多线程)。每个阶段是一个小区块，相关的数据按列对齐排成两行。当前阶段原地刷新(进度条推进，速率、传输量、负载时延实时更新)，测完定格，再出现下一个阶段：

```
  SpeedTaier v0.6.2

  INFO    本机出口 39.78.60.193 · 中国 山东 济南 · 电信

  INFO    测试 电信 IPv4 济南电信 (58.56.118.126)

          空闲时延                      ━━━━━━━━━━━━━━━━━━━━
          时延 5.9 ms           采样 20 次
          抖动 2.8 ms           最低 4.6 ms           最高 13.7 ms

          下载 · 单线程 · 1 条连接      ━━━━━━━━━━━━━━━━━━━━
          速率 971.7 Mbps       传输 619.8 MB         用时 5.3 秒
          负载时延 14.6 ms      负载抖动 4.1 ms       负载最高 26.9 ms

          下载 · 多线程 · 8 条连接      ━━━━━━━━━━━━━━━━━━━━
          速率 801.2 Mbps       传输 300.0 MB         用时 3.0 秒
          负载时延 15.4 ms      负载抖动 1.8 ms       负载最高 20.0 ms   ← 正在测

  INFO    消耗流量 1.4 GB · 总用时 25 秒
```

状态行带彩色标签：`INFO`(青色)是信息，`WARNING`(黄色)是警告与中断，`ERROR`(红色)是错误；阶段区块本身不带标签。阶段的标题与进度条按类型固定颜色：空闲时延绿色、下载青色、上传洋红；数据一律用白色，数据标签与单位为灰色。

| 项 | 含义 |
|---|---|
| 本机出口 | 本机出口 IP、归属地与运营商，用来选择测速节点 |
| 测试 | 当前测速的节点：运营商、协议(IPv4 / IPv6)、节点名与地址 |
| 空闲时延：时延 | 没有传输数据时的往返时延(中位数)。采样期间随每个样本更新，进度条为采样进度 |
| 采样 | 采样次数，用 `--latency-count` 调整(默认 20)。有失败时同一行多一项失败率(超时、断开的比例，**不等同于丢包率**) |
| 抖动 | 按时间顺序相邻两次往返时延之差的平均，越小越稳定；至少要两个样本 |
| 最低 / 最高 | 采样中往返时延的最小值与最大值 |
| 单线程 / 多线程 | 单线程用 1 条 TCP 连接，多线程用 `--threads` 条(默认 8)，连接数写在标题里。多线程接近线路的总带宽；单线程受延迟和丢包影响更大，二者差距大说明线路上存在单连接瓶颈 |
| 速率 | 预热结束后的平均吞吐，是最终结果。测速期间显示当前速率，进度条按最长测速时长估算，吞吐收敛后会提前走满 |
| 传输 · 用时 | 该阶段实际传输的数据量与总用时，**都包含预热期**(开头的慢启动)，所以两者相除会略低于速率，二者口径一致。各阶段的传输量之和就是页脚的消耗流量 |
| 负载时延 · 负载抖动 · 负载最高 | 下载、上传进行时的往返时延，即线路被占满时的延迟；明显高于空闲时延说明链路存在缓冲膨胀。测速一开始就实时展示(预热期间含到目前为止的全部样本)，预热结束后只统计预热之后的样本，**最终结果不含预热期**，所以数值可能在预热结束时略有变化 |

数据暂时没有时显示 `-`。终端较窄时各列会收紧，放不下的项(从最后一项开始)和进度条会省略。

某个节点测速失败时会换下一个节点；一个运营商的所有节点都失败时，末尾用 `ERROR` 给出最后一个节点的失败原因。

**中断**：测速中按 Ctrl-C，已经测完的阶段留在屏幕上，进行中和还没开始的阶段不显示，最后用一行 `WARNING 已中断` 收尾(上方空一行)。

进度和提示写到 stderr，测速结果(节点、各阶段区块、流量统计)写到 stdout，因此 `speedtaier > result.txt` 只保存结果。stderr 不是终端、使用 `--no-progress` 或设置了 `NO_COLOR` 时，分别不刷新实时进度、不输出颜色；此时各阶段仍然是测完一个输出一个。终端高度不足 12 行时同样不做原地刷新。

`speedtaier list` 列出全省节点及连通性(测速时不显示)：

```
城市 │ 运营商 │ 节点     │ IPv4    │ IPv6
南京 │ 电信   │ 南京电信 │ 26.6 ms │ 未探测
南通 │ 移动   │ 南通移动 │ 31.9 ms │ -
```

- `xx ms`：应用层往返时延；`不可达`：有地址但无法完成测速协议的请求；`-`：节点没有该协议的地址；`未探测`：本机没有该协议的路由。

## 测速方法

1. **选节点**：按 IP 归属地确定省份、运营商与所在城市，并发探测本省本运营商的节点。本市的可达节点排在前面，其余按时延从低到高，依次尝试，首个测速成功的节点即为结果；本市没有可达节点时测本省其他城市时延最低的节点。只测本机运营商，是因为跨运营商测速受网间互联带宽限制，反映不了本机线路。
   - 归属地查不到运营商，或该省没有本机运营商的节点：不区分运营商，测时延最低的节点。
   - 归属地查不到省份(如海外网络、接口异常)：报错退出，需要用 `--province` 指定。
   - `--operator all`：每个运营商各选一个节点测速。
2. **空载时延**：在一条长连接上连续请求(默认 20 次)，丢弃首次(含建连)，统计其余样本。
3. **预热**：下载前 1.5 秒、上传前 3 秒的数据不计入结果，以排除 TCP 慢启动与发送缓冲区填充的影响。
4. **收敛判定**：预热后每秒统计一次窗口吞吐，连续 3 个窗口都在其均值 ±5% 以内即提前结束；每阶段最短 3 秒、最长 `--seconds` 秒(默认 10)。
5. **负载时延**：传输期间另开一条连接，每 100 毫秒测量一次往返时延；最终结果只统计预热之后的样本，实时展示则从第一个样本开始。
6. **多连接**：默认 8 条连接错峰 200 毫秒启动，单条断开后自动重连；结束时以 RST 关闭连接，避免残余数据在测速后继续占用带宽。下载、上传各先用 1 条连接测一次，再用 `--threads` 条连接测一次；`--no-single` 跳过单线程，`--threads 1` 只测单线程。

在千兆宽带上，一个运营商一个协议族约 25 秒、消耗 1–1.5 GB 流量(本机有 IPv6 时 IPv4、IPv6 各测一次，加倍)；`--no-single` 约 15 秒、减半；`--operator all` 为运营商数量的倍数。

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

一键安装脚本会自动选择合适的包。程序为静态编译，不依赖系统 C 库，测速时内存占用约 30 MB。MIPS 架构的硬路由(如 MT7621)暂不支持。

注意事项：

- **存储空间**：解压后约 9–11 MB。闪存较小(如 16 MB)的设备可以安装到内存盘，重启后需要重新安装：
  ```bash
  wget -qO- https://raw.githubusercontent.com/cosxs/speedtaier-cli/main/scripts/install.sh | INSTALL_DIR=/tmp sh
  ```
- **CPU 可能成为瓶颈**：性能较弱的设备(如 Cortex-A53 级 ARM)可能跑不满千兆。测速时可用 `top` 观察，CPU 占满说明结果受限于路由器本身。
- **PPPoE 拨号与代理插件**：PPPoE 的 WAN 口(如 `pppoe-wan`)不会被误判为代理；OpenClash 等插件的 TUN 模式会被识别，测速连接自动绑定 WAN 口绕开代理。只代理局域网设备流量的 iptables/nftables 透明代理不影响路由器自身的测速。
- **定时测速**：输出不是终端时自动输出纯文本(无颜色、无实时刷新)，可以直接追加到日志。建议指定省份，避免归属地接口异常时无法确定省份。千兆宽带每次测速约消耗 1–1.5 GB 流量(`--no-single` 减半，本机有 IPv6 或使用 `--operator all` 则成倍增加)，请控制频率。以 OpenWrt 为例，执行 `crontab -e` 添加：
  ```
  0 4 * * * /usr/bin/speedtaier --province jiangsu >> /root/speedtaier.log 2>&1
  ```

## 退出码

| 退出码 | 含义 |
|---|---|
| 0 | 全部成功 |
| 1 | 启动失败或参数错误 |
| 2 | 部分测试失败，或存在 `WARNING` 警告 |
| 130 | 用户中断 |

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--province` | IP 归属地 | 省份拼音；归属地查不到省份时必须指定 |
| `--family` | `auto` | `auto`(本机有 IPv6 时加测 IPv6)、`4`、`6` |
| `--operator` | `auto` | `auto`(本机运营商，按 IP 归属地)、`all`(全部运营商)或运营商名，如 `电信`、`liantong`、`mobile` |
| `--interface` | `auto` | `auto`、`none` 或网卡名，见[透明代理](#透明代理) |
| `--seconds` | `10` | 每阶段最长计量秒数(不含预热)，吞吐稳定后提前结束 |
| `--threads` | `8` | 多线程测速的并发连接数；`1` 表示只测单线程 |
| `--no-single` | | 不测单线程，只测多线程 |
| `--latency-count` | `20` | 空载时延采样次数，抖动按这些样本计算 |
| `--timeout` | `0` | 单节点总超时秒，0 表示按测速时长自动计算 |
| `--no-download` / `--no-upload` | | 跳过下载 / 上传 |
| `--no-progress` | | 关闭实时进度 |
| `--no-update-check` | | 不检查新版本，见[升级](#升级) |

设置环境变量 `NO_COLOR` 可关闭彩色输出。

## 网络访问

工具会访问以下地址：

| 地址 | 用途 |
|---|---|
| `dlcv2.cnspeedtest.cn:8443` | 配置清单、IP 归属地、IPv6 节点匹配 |
| `down.cnspeedtest.cn:8043`(以配置清单为准) | 加密的全量节点库 |
| 各测速节点 TCP `65499`、`9443` 端口 | 时延探测、鉴权、下载、上传 |
| `api.github.com` | 检查新版本：测速结束时在后台访问(仅终端中运行、24 小时一次)，`upgrade` 时也会访问；可关闭，见[升级](#升级) |
| `github.com` | 仅 `upgrade` 使用：下载发布包与校验文件 |
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
  cli/       命令行参数、终端界面、升级
  runner/    编排：选省份与运营商 → 选出口网卡 → 探测 → 按运营商测速；产出 Result
  taier/     全球网测协议与测速引擎(计量、时延、探测)
  netx/      平台相关网络能力：绑定网卡、查路由表、检测透明代理
  fakenode/  模拟测速节点，供测试使用
```

## 许可证

[MIT](LICENSE)
