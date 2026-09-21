# termfana

[![CI](https://github.com/laixintao/termfana/actions/workflows/ci.yml/badge.svg)](https://github.com/laixintao/termfana/actions/workflows/ci.yml)

在终端中直接读取程序的 `/metrics`，查看指标趋势、吞吐和延迟，用于 SSH 会话中的临时排障。

一个二进制、一个 metrics URL 即可开始。程序自己定时采样，历史保存在内存中。

## 快速开始

编译需要 Go 1.26 或更新版本；编译后的二进制不依赖 Go 运行环境。

可以从 [GitHub Releases](https://github.com/laixintao/termfana/releases) 下载 Linux/macOS、amd64/arm64 对应的 `.tar.gz`，解压后直接运行 `./termfana`。每次发布附带 `SHA256SUMS`：Linux 用 `sha256sum --check SHA256SUMS`，macOS 用 `shasum -a 256 --check SHA256SUMS` 校验（校验全部文件需要下载四个包）。

也可以用 Go 安装：`go install github.com/laixintao/termfana/cmd/termfana@latest`。

```sh
make build

# 无需连接实际程序，体验流量、延迟尖峰和计数器重置
./bin/termfana demo

# 连接程序的完整 /metrics 地址
./bin/termfana http://localhost:8080/metrics

# 更快采样，保留最近 600 轮，初始显示 2 分钟
./bin/termfana --interval 1s --capacity 600 --window 2m \
  http://localhost:8080/metrics
```

启动后搜索指标，按 Enter 加入面板。用 `a` 再添加相关指标，最多同时打开 4 个面板。采集从启动时开始，因此稍后添加的指标也能查看已保留的历史。

宽屏显示双列面板；80×24 终端显示当前面板，用 Tab 或数字键切换。Braille 字体显示不佳时使用 `--ascii`。

## 排障操作

| 按键 | 操作 |
| --- | --- |
| `a` / `/` | 打开指标浏览器 / 搜索 |
| `Enter` | 添加指标；在看板中放大当前面板 |
| `Tab` / `Shift+Tab` / `1`–`4` | 切换面板 |
| `v` | 切换原值、速率或 Histogram 视图 |
| `l` | 筛选标签：Enter 勾选，`c` 清空，Esc 返回 |
| `g` | 查看 series 列表；Enter 查看完整标签和游标值，Space 隐藏/显示 |
| `←` / `→` | 查看同一采样时刻，各面板共享游标 |
| `+` / `-` | 缩放时间范围 |
| `[` / `]` | 向前 / 向后平移时间范围 |
| `Space` | 冻结画面 / 返回实时，后台继续采集 |
| `r` / `Home` | 返回实时 |
| `d` | 删除当前面板 |
| `s` | 保存会话配置，指定已有文件时替换该文件 |
| `i` | 浏览器中查看指标完整说明 |
| `?` | 快捷键帮助 |
| `q` / `Ctrl+C` | 退出并恢复终端 |

每个面板最多绘制 8 条曲线，底部标明显示数量。用标签筛选或隐藏其他 series，选择需要对比的曲线；`g` 中可以查看具体值和完整标签。默认每 5 秒采样，保留 360 轮，约 30 分钟；更改间隔会改变这 360 轮覆盖的时间。

## 指标计算

| 类型 | 展示方式 |
| --- | --- |
| Gauge / 未声明 TYPE | 原始数值 |
| Counter | 默认 `rate`：相邻两次采样的增量 ÷ 实际秒数；也可查看 `raw` |
| 经典 Histogram | 默认 `p95`；可选 `p50`、`p99`、`mean`、`rate`、`raw` |
| Summary | 已暴露的 quantile、sum、count 原值 |

- Histogram 分位数根据两轮采样的累计桶差值进行桶内线性插值，图中以 `≈` 标明估算。`mean` 是 `Δsum / Δcount`，`rate` 是 `Δcount / Δtime`。
- Histogram 按除 `le` 外的标签分别计算；Summary 的 quantile 不做聚合或重新计算。
- 首轮、Counter 回退、创建时间变化、断采或 series 重新出现时，派生值重新建立基线。没有新请求的 Histogram 分位数和均值显示 `N/A`，请求速率为 0。
- 失败、缺失、`NaN`、`Inf` 和不可计算的区间保留为缺口。图形不会把缺口补零，也不会跨缺口连线。
- 全部曲线按本地采集完成时刻对齐。端点附带的样本时间戳不用于横轴或速率计算。
- 工具只能观察采样间发生的变化；未暴露创建时间、且重置后计数已超过上次值的 Counter，无法可靠识别这次重置。

支持 Prometheus text 和 OpenMetrics 1.0，包括 HELP、TYPE、UNIT 及转义标签；exemplar 可被解析，但不展示。首版只计算经典 Histogram，不支持原生 Histogram、PromQL 或跨 series 聚合。

## 脚本与管道

选项放在 URL 前面。

```sh
# 列出可用指标和类型
./bin/termfana list http://localhost:8080/metrics

# 指标目录输出为一个 JSON 数组
./bin/termfana list --format json http://localhost:8080/metrics

# 单轮原始值
./bin/termfana sample --metric process_resident_memory_bytes \
  http://localhost:8080/metrics

# 每轮输出一个 JSON 对象；可直接使用 jq 或写文件
./bin/termfana sample --metric http_requests_total \
  --view rate --label method=GET --label status=500 \
  --interval 1s --count 12 --format json \
  http://localhost:8080/metrics | jq .

# Histogram 的 p95，以完整 family 名称指定
./bin/termfana sample --metric http_request_duration_seconds \
  --view p95 --count 6 --format json http://localhost:8080/metrics

# 持续输出，Ctrl+C 结束
./bin/termfana sample --metric workers --count 0 --format json \
  http://localhost:8080/metrics > workers.jsonl
```

`sample` 默认 `raw`、输出一轮。派生视图先执行一轮基线采样，再输出 `--count` 指定的轮数；两次采样间遵循 `--interval`。标签匹配为精确匹配，多个标签条件使用 AND。

JSON Lines 的一轮示例：

```json
{"timestamp":"2026-09-21T14:00:05+08:00","duration_ms":2.4,"view":"rate","status":"ok","samples":[{"metric":"http_requests_total","labels":{"method":"GET","status":"500"},"value":1.2,"status":"ok"}]}
```

不可用数值为 `null`，sample 的 `status` 说明原因，如 `warming_up`、`reset`、`non_finite`、`no_observations`。采集失败时该轮 `status` 为 `scrape_failed`，`samples` 为空，`error` 给出原因；下轮继续采集。没有匹配标签时返回 `no_matches`。

数据写 stdout，诊断写 stderr。退出码：`0` 正常结束，`1` 采集或输出失败，`2` 参数/配置错误。连续采样中出现过采集失败，最终退出码仍为 `1`。交互模式要求 TTY；重定向和管道请使用 `list` 或 `sample`。

## 会话与接入

按 `s` 保存面板、标签、视图、端点和采样配置，然后恢复：

```sh
./bin/termfana --session debug.json
```

会话是版本化 JSON，仅包含配置，不包含采样历史。保存使用原子替换，文件权限为 `0600`。命令行显式选项覆盖会话中的对应设置。

无鉴权的程序只需要 URL；Bearer 和 Basic 通过环境变量提供。会话仅保存环境变量名称。

```sh
# 使用已经设置的 APP_METRICS_TOKEN
./bin/termfana --token-env APP_METRICS_TOKEN https://service.example/metrics

# 使用已经设置的 APP_METRICS_USER / APP_METRICS_PASSWORD
./bin/termfana --username-env APP_METRICS_USER \
  --password-env APP_METRICS_PASSWORD https://service.example/metrics
```

HTTPS 使用系统信任根；已有 SSH 端口转发可直接作为本地 URL 使用。程序不实现登录流程或自动创建 SSH 隧道。

默认请求超时 3 秒，不重叠采集；错误后在下一周期重试。解压后单次响应最多 16 MiB、最多 10,000 条 series，可通过 `--max-bytes`、`--max-series` 调整。超限整轮报错，避免把截断结果当作完整采样。保留历史中的 series 目录也受相同数量上限约束；标签大量变化时会提前淘汰旧历史，并在界面提示。

## 开发与验证

```sh
make build           # bin/termfana
make check           # go test -race ./... + go vet ./...
make smoke           # 标准库 Python 伪终端端到端测试（Linux/macOS）
make dist            # dist/ 下的 Linux/macOS × amd64/arm64 单二进制
make package         # 四个平台的版本化 tar.gz + SHA256SUMS
```

测试涵盖格式解析、HTTP 鉴权/超时、Counter 重置、Histogram 区间计算、内存淘汰、CLI JSON、终端尺寸和键盘流程。`make smoke` 会启动临时 localhost 服务，验证四面板、标签筛选、断采恢复、保存/恢复、窗口缩放，以及正常退出、SIGINT、SIGTERM 后的终端恢复。

需要一个可供其他命令测试的示例端点时：

```sh
./bin/termfana demo --serve
# 打印临时 loopback /metrics URL；Ctrl+C 停止
```

实现按 `metrics`（采集、解析、历史、计算）、`chart`（字符绘图）、`tui`、`cli`、`config`、`demo` 分层。TUI 与 CLI 共用采集和计算核心，运行时不连接任何 Prometheus 服务。

## 版本与自动发布

维护者安装一次 [bump2version](https://github.com/c4urself/bump2version)，它提供 `bumpversion` 命令：

```sh
pipx install bump2version==1.0.1
```

提交代码后，在工作区干净的分支执行：

```sh
make release PART=patch       # 例如 0.1.0 → 0.1.1；默认 patch
# 或 make release PART=minor  # 0.1.0 → 0.2.0
# 或 make release VERSION=0.2.0
```

该命令调用 bumpversion，同步 `.bumpversion.cfg` 和 CLI 的版本号，创建版本 commit 与 `vX.Y.Z` annotated tag，再通过 atomic push 把当前分支和这个 tag 一起推到 `origin`。若推送失败，本地 commit/tag 会保留；按输出提示修复并重试 push，无需再次 bump。

也支持直接使用 bumpversion，分开操作：

```sh
bumpversion patch
git push --atomic origin HEAD --follow-tags
```

本地 bumpversion 不会访问 GitHub；tag 推送后才会触发自动发布。普通分支 push 和 PR 会执行 Linux/macOS 的 race tests、`go vet`、版本校验、发布脚本测试及真实伪终端测试。版本 tag 使用同一套测试，全部通过后构建 Linux/macOS × amd64/arm64 的安装包，再发布 GitHub Release，附带校验和及自动生成的 release notes。无需配置额外 secret，发布使用仓库自带的 `GITHUB_TOKEN`。

版本、源码或 tag 不一致会阻止发布。当前支持稳定版 `major.minor.patch`。仅修改源码中的版本号不会发布，必须推送对应的 tag。

可以在 [Release workflow](https://github.com/laixintao/termfana/actions/workflows/release.yml) 对分支手动 Run workflow：测试和打包完成后生成 `release-assets` artifact，不创建正式 Release。`master` 上修改发布 workflow 或打包脚本时也会自动执行这项验证。手动运行在版本 tag 上则会发布该版本。

本地验证发布脚本（只操作临时仓库）：

```sh
python3 -m pip install -r scripts/requirements-ci.txt
make release-test
```
