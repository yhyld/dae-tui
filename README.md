# dae-tui

dae 网络代理的终端管理界面（TUI），当前通过 **daed 的 GraphQL API** 管理代理：切换路由组节点、测速、实时流量、订阅管理、config/DNS/routing 方案切换与应用。

```
┌ dae-tui (http://127.0.0.1:2023/graphql)  ● 运行中 dae v2.1.1   ↑12KB/s ↓1.9MB/s ┐
│ 1 首页  2 群组  3 订阅  4 手动节点  5 配置  ? 帮助                               │
│  路由组 (3)                        │  proxy · 自动 (最小移动平均延迟)            │
│  ─────────────────────────────    │  ────────────────────────────────────────  │
│  ❯ proxy               3节点      │  ├· 订阅 机场A              24/24节点  x    │
│    direct              0节点      │  ├· 订阅 机场B               0/10节点  x    │
│    pinned              1节点      │  ├· 直接添加的节点                          │
│                                    │  （Enter 展开分区；节点行 Enter 固定）      │
│  j/k 移动  Tab/l 展开 …                                                          │
└──────────────────────────────────────────────────────────────────────────────┘
```

## 构建

```bash
go build -o dae-tui ./cmd/dae-tui
```

依赖：Go 1.27.1+（go.mod 即此版本）。仅 5 个直接依赖：bubbletea / bubbles /
lipgloss / toml / charmbracelet-x-ansi（终端单元格宽度计算，与 lipgloss 同口径，
保证中文与国旗 emoji 占两列时列对齐）。

## 使用

```bash
./dae-tui                    # 连接 http://127.0.0.1:2023/graphql（默认）
./dae-tui -endpoint http://127.0.0.1:2024/graphql
./dae-tui -probe             # 非交互自检（只读打印后端状态）
./dae-tui -config PATH       # 指定配置文件
```

首次启动：若 daed 尚无账号则进入初始化表单（创建账号），否则显示登录表单。登录成功后
用户名/密码/JWT（30 天有效期）保存在 `~/.config/dae-tui/config.toml`（权限 0600），
token 过期后自动用保存的凭据静默续期。

### 按键

所有内容页均为 **左列表 + 右详情** 双栏：左栏 `j/k` 移动（默认折叠），`Tab/l/Enter`
展开到右侧详情（右侧有自己的高度与滚动），`h/esc` 折叠返回左栏。

| 页面 | 键 | 功能 |
|---|---|---|
| 全局 | `1/2/3/4/5` | 首页 · 群组 · 订阅 · 手动节点 · 配置 |
| 全局 | `A` | 应用当前选中 config+dns+routing（任何页面可用，需 `y` 确认） |
| 全局 | `?` `r` `q` | 帮助 · 刷新当前页 · 退出 |
| 全局 | — | 启动时自动测速一次；延迟数据每 3 秒轮询（按需手动 `t`/`T` 触发） |
| 首页 | `o` | 启动/停止代理（`run`；停止=dry，需 `y` 确认） |
| 首页 | — | 实时流量图、各组当前节点（固定=精确；自动=按已测延迟估计 `≈`） |
| 群组 | `Enter`（右栏节点行） | 固定到该节点：daed v2 的 fixed 组只允许一个成员，操作会移除组内其他订阅挂载与节点、仅保留该节点（需 `y` 确认；恢复用 `s` 重新挂订阅） |
| 群组 | `c` `R` `D` | 创建群组 / 重命名 / 删除（确认） |
| 群组 | `p` / `a` | 修改群组策略 / 快捷切回自动策略 |
| 群组 | `Enter`（分区头） | 展开/收起订阅或直接节点分区（默认收起） |
| 群组 | `t` / `T` | 整组或订阅内测速 / 单节点测速 |
| 群组 | `s` / `n` / `x` | 挂订阅 / 加节点（手动 + 订阅内节点）/ 移除订阅或直接挂载的节点 |
| 手动节点 | `a` `x` `t` | 导入分享链接 / 删除 / 测速 |
| 手动节点 | `Tab` 后 `G` | 把选中节点加入群组 |
| 订阅 | `t`（右栏） | 对该订阅全部节点测速 |
| 订阅 | `u` `n` `x` `c` | 更新 / 新增 / 删除（确认）/ 编辑定时刷新 cron（表达式+开关） |
| 配置 | `Enter` | 切换选中项（应用用全局 `A`）。注意：daed 的 run(dry) 是"停止代理"而非校验，已移除该键 |
| 配置 | `c` `R` `D` | 新建（克隆当前选中配置）/ 重命名 / 删除（确认；选中的与最后一个条目受保护） |
| 配置 | `e` | config 逐字段编辑（部分更新 globalInput）；DNS/路由调 `$EDITOR` 编辑 DSL 原文，退出后自动提交变更 |
| 配置 | `Tab` 后 `j/k` | 滚动查看右栏内容（config 摘要 / DNS、路由 DSL 原文） |

## 架构

```
cmd/dae-tui          入口（flag、probe 模式）
internal/driver/     后端抽象：Driver 接口 + 领域类型（Group/Node/Latency/Traffic…）
internal/driver/daed daed GraphQL 驱动（client/auth/queries/driver）
                     schema.graphql = daed v2.1.1 最终版 SDL 的固化副本
internal/app/        Bubble Tea 应用：根模型（连接→登录→主界面）、各页面
internal/ui/         样式、延迟色阶、格式化、braille 迷你图
internal/config/     ~/.config/dae-tui/config.toml（0600）
```

**可插拔 driver** 是本项目的核心设计：UI 只依赖 `driver.Driver` 接口。daed 驱动为
Phase 1；规划中的 Phase 2 是"裸 dae 驱动"（编辑 `/etc/dae/config.dae` + `dae
validate/reload` + journald 日志，`dae reload` 为 SIGUSR1 + 进度文件轮询），远期可加
clash-api 驱动（honk / mihomo）。纯网络客户端不链接 dae/daed 源码，因此本仓库可保持
宽松许可证（dae 本体与 dae-wing 均为 AGPL-3.0，仅在其进程边界之外交互）。

### daed API 要点（对二次开发有用）

- 单一 `POST /graphql`，`Authorization: Bearer <JWT>`，无 WebSocket/cookie/CSRF；
  认证失败表现为 HTTP 200 内的 GraphQL error `"access denied"`（本客户端自动重认证重试一次）。
- 切节点：`groupSetPolicy(id, policy: fixed, policyParams: [{val: "<index>"}])`，
  key 留空即渲染为 dae DSL 的位置参数 `fixed(<index>)`；index 相对 `group.nodes` 顺序。
- 流量：轮询 `general { runtimeOverview(windowSec, maxPoints) }`；注意
  `uploadTotal/downloadTotal` 在 SDL 中是 String。
- 测速：`testNodeLatencies(ids)` 触发，`nodeLatencies(ids)` 读回（以 `testedAt` 变新为准）。
- schema 以 `daed export schema` 导出，或运行时 `general { schema }` / introspection。

## 开发与测试

```bash
go test ./...          # driver 单测（httptest mock GraphQL）+ TUI 无头渲染冒烟测试
# 无副作用联调：临时目录 + 独立端口起一个无 eBPF 的纯 API 实例
daed run --api-only -l 127.0.0.1:2024 -c /tmp/daed-dev
dae-tui -probe -endpoint http://127.0.0.1:2024/graphql
```

`-probe` 全程只读，唯一例外是 `DAE_TUI_PROBE_MUTATE=1` 会额外触发一次全节点测速
（`testNodeLatencies`）。它**永远不会调用 `run()`**：daed 的 `run(dry:true)` 是停止代理
而不是校验，自检不能把用户的代理停掉。

已知限制：`--api-only` 实例没有运行中的 dae 核心，`testNodeLatencies`/`nodeLatencies`
会返回 "record not found"；测速需在真实运行的 daed 上验证。

## 安全须知（重要）

1. **daed 默认监听 `0.0.0.0:2023`**（所有网卡），纯 HTTP + CORS 全开。管理 API 暴露在
   局域网。建议收紧为仅回环（需重启 daed，瞬断几秒）：

   ```bash
   # systemd drop-in
   sudo systemctl edit daed
   # 写入:
   # [Service]
   # ExecStart=
   # ExecStart=/usr/bin/daed run -c /etc/daed/ -l 127.0.0.1:2023
   sudo systemctl daemon-reload && sudo systemctl restart daed
   ```

   首次运行前还有"抢注"窗口：谁能先访问 2023 端口谁就能创建管理员账号——所以应先绑
   回环再首次启动。
2. **远程管理**用 SSH 隧道，不要把明文 HTTP API 暴露到网络：
   `ssh -L 2023:127.0.0.1:2023 <host>`，然后本工具用默认端点即可。
3. 本工具的配置文件含密码与长期 JWT，已设 0600；如怀疑泄露在 daed web UI 改密码
   （会使旧 JWT 失效）。

## 兼容性说明

daed 与其后端 dae-wing 已于 2026-09-24 归档，v2.1.1 为最终版本（bundle dae v2.1.x）。
其 GraphQL API 从此冻结：好处是永远不会破坏变更，坏处是上游不再有安全修复。本工具按
v2.1.1 schema 开发（SDL 已固化在仓库中做回归对照）。dae 本体仍在活跃维护；若未来迁移
到裸 dae，见上文 Phase 2 计划。
