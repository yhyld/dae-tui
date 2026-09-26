# dae-tui

dae 网络代理的终端管理界面（TUI），当前通过 **daed 的 GraphQL API** 管理代理：切换路由组节点、测速、实时流量、订阅管理、节点/订阅原地编辑、config/DNS/routing 方案切换与应用、账户管理。

```
┌ dae-tui (http://127.0.0.1:2023/graphql)  ● 运行中 dae v2.1.1   ↑12KB/s ↓1.9MB/s ┐
│ 1 首页  2 群组  3 订阅  4 手动节点  5 配置  ? 帮助                               │
│  路由组 (3)                        │  proxy · 自动 (最小移动平均延迟)            │
│  ─────────────────────────────    │  ────────────────────────────────────────  │
│  ❯ proxy               3节点      │  ├· 订阅 机场A              24/24节点  x    │
│    direct              0节点      │  ├· 订阅 机场B               0/10节点  x    │
│    pinned              1节点      │  ├· 直接添加的节点                          │
│                                    │  （Enter 展开/收起分区）                     │
│  j/k 移动  Tab/l 展开 …                                                          │
└──────────────────────────────────────────────────────────────────────────────┘
```

## 路由简单模式（首页快速切换）

路由 DSL 对新手不友好，因此首页提供一个**预设选择器**（对应 daed web UI 的 simple
mode）：4 个常用模式一键切换，确认前会展示**即将写入的完整 DSL**，写入前过后端语法
校验，全程不需要手写 DSL。自定义规则显示为「自定义规则」，随时可以用 `e`（配置页）回到
进阶编辑。

| 预设 | 生成的规则（公共前缀见下） | 效果 |
|---|---|---|
| GFW 模式 | `domain(geosite:gfw) -> 组` + `fallback: direct` | 仅代理被墙域名 |
| 中国列表以外 | `dip/domain(geoip/geosite:cn) -> direct` + `fallback: 组` | 国内直连，其余代理 |
| 中国列表 | `dip/domain(geoip/geosite:cn) -> 组` + `fallback: direct` | 国内走代理，其余直连 |
| 全局代理 | `fallback: 组` | 全部走代理 |

公共前缀（所有预设都有，保证本机与私网流量不进代理）：

```
pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct
dip(geoip:private) -> direct
```

要点：

- **代理组**：默认取当前路由已在引用的组（fallback 或规则 outbound），没有则取第一个
  非 direct 组；`g` 可循环切换，预览随之更新。
- **破坏性**：切换会整体替换该路由方案的规则，因此必须 `y` 确认，且确认框里就是最终
  DSL——没有隐藏动作。
- **生效**：预设只改“选中的路由方案”内容，代理要在 `A` 应用后才按新规则运行（状态栏
  `⚠ 未应用` 会提示）。
- **识别**：启动/刷新时会把当前路由方案反解析成预设（要求公共前缀存在且所有规则都是
  预设自身能产生的），识别不了就是自定义——不会把相像的手写规则错标成预设。

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
./dae-tui -version           # 打印版本
./dae-tui -cmd status        # 非交互：一行式后端状态（版本/运行态/各组策略与当前节点/选中方案）
./dae-tui -cmd test          # 非交互：触发全部节点测速
./dae-tui -cmd test -g proxy # 只测指定组（逗号分隔多个组）的全部成员
./dae-tui -cmd test -n id1,id2   # 只测指定节点 ID
```

`-cmd` 子命令可绑系统快捷键、写进脚本。刻意没有提供 `-cmd switch`：daed v2 的 fixed 组
只允许一个成员，"切到某节点"会静默重组整组（摘订阅、移除其他直接节点），破坏性太大，
不适合无人值守执行。

首次启动：若 daed 尚无账号则进入初始化表单（创建账号），否则显示登录表单。登录成功后
用户名/密码/JWT（30 天有效期）保存在 `~/.config/dae-tui/config.toml`（权限 0600），
token 过期后自动用保存的凭据静默续期。首页 `P` 可修改密码（旧 JWT 失效、新 token 自动
落盘）或退出登录（清除本机保存的密码与 token，回到登录表单）。密码规则（至少 6 位且
含字母和数字）在客户端与服务端一致校验。

### 按键

所有内容页均为 **左列表 + 右详情** 双栏：左栏 `j/k` 移动（默认折叠），`Tab/l/Enter`
展开到右侧详情（右侧有自己的高度与滚动），`h/esc` 折叠返回左栏。

整个界面渲染在一个圆角外框内并**恰好填满终端**（帮助条始终钉在最后一行，面板不足时
补齐空行）；终端小于 60×12 时显示"终端太小"提示页，放大即恢复。**浮窗**：帮助（`?`，
任意页面）、账户（`P`，任意页面）、全局 `A` 应用确认、首页预设切换确认（含 DSL 预览）
与各页输入表单（订阅新增/编辑/定时刷新、手动节点导入/编辑、群组创建/改名、配置新建/
改名/字段编辑）都渲染为悬浮在页面之上的圆角框，页面内容不再被顶开；小 y/n 确认和大列
表选择器（如加节点选择器）保留在右栏——决策和填表用浮窗，浏览和对比用面板。首页内容
超高时窗口自动跟随焦点行（滚轮可临时自由滚动，任意按键恢复跟随）。

**鼠标**：滚轮滚动当前列表/页面（等价 `j/k`，每格 3 行）；点击顶部页签切换页面，
点击左栏行选中该项，点击底部键位行打开帮助浮窗；弹窗/输入框打开时鼠标输入忽略。

| 页面 | 键 | 功能 |
|---|---|---|
| 全局 | `1/2/3/4/5` | 首页 · 群组 · 订阅 · 手动节点 · 配置 |
| 全局 | `A` | 应用当前选中 config+dns+routing（任何页面可用，需 `y` 确认） |
| 全局 | `?` `r` `q` | 帮助浮窗（j/k 滚动） · 刷新（重拉全部列表数据，不止当前页——各页共享组/订阅/方案数据） · 退出 |
| 全局 | `P` | 账户浮窗（任意页面）：修改密码 / 退出登录（清除本机保存的密码与 token） |
| 全局 | — | 测速按需触发（`t`/`T`，只测当前列表/组/订阅）；延迟数据每 3 秒轮询**当前页可见节点**，流量每秒刷新。测速中时页签栏显示旋转指示与"已回 x/y"（跨页汇总，切页不消失），等待窗口按节点数放宽（15s + 200ms/节点，上限 2 分钟） |
| 首页 | `o` | 启动/停止代理（`run`；停止=dry，需 `y` 确认） |
| 首页 | `j/k` `Enter` | 路由快速切换：选预设替换当前路由方案（先展示将写入的 DSL，需 `y` 确认） |
| 首页 | `Tab` | 在「路由快速切换」与「各组当前节点」之间切换焦点 |
| 首页 | `Enter`（组行） | 跳到群组页并展开该组——切换某组节点的最短路径（固定节点策略的取舍见"已知限制"） |
| 首页 | `L` | 查看 daed 日志（`journalctl -u daed -f` 只读跟随；仅 daed 跑在本机时可用，远程隧道场景无效） |
| 首页 | `g` | 切换预设使用的代理组（默认取当前路由已在引用的组） |
| 首页 | — | 实时流量图与路由快速切换在宽终端（≥98 列）并排两栏、窄终端上下堆叠；各组当前节点（fixed=精确节点名；自动=已测最优节点的估算 `≈`，无测量数据时标注「未测速」，**均不含毫秒**）、网络状态（各网卡地址与默认路由；配置里写了的网卡不存在时告警） |
| 群组 | `space` | 标记/取消标记节点：`t` 只测已标记、`x` 批量移除已标记的直接节点；选择器内 `Enter` 批量添加已标记候选 |
| 群组 | `c` `R` `D` | 创建群组 / 重命名 / 删除（确认）。组被路由方案引用时，`R`/`D` 确认框会点名——改名/删除会让那些规则静默失效 |
| 群组 | `p` / `a` | 修改群组策略 / 快捷切回自动策略 |
| 群组 | `Enter`（分区头） | 展开/收起订阅或直接节点分区（默认收起；展开状态按订阅 ID 记忆，刷新重排不错位） |
| 群组 | `t` / `T` | 整组或订阅内测速 / 单节点测速 |
| 群组 | `s` / `n` / `x` | 挂订阅 / 加节点（手动 + 订阅内节点）/ 移除订阅或直接挂载的节点 |
| 手动节点 | `a` | 批量导入：每行一个分享链接可整段粘贴；坏链接逐条报错（右栏列明细），不影响其他链接 |
| 手动节点 | `e` | 编辑节点标签与链接（`tagNode`/`updateNode`，保留节点 ID：删了重导会丢群组挂载） |
| 手动节点 | `x` `t` `y` | 删除 / 测速 / 复制分享链接到剪贴板（OSC 52，SSH 下也可用） |
| 手动节点 | — | 详情页「趋势」：该节点最近约 3 分钟的延迟迷你图（客户端累积采样，能看出是否在持续变慢） |
| 手动节点 | `Tab` 后 `G` | 把选中节点加入群组 |
| 订阅 | `t`（右栏） | 对该订阅全部节点测速 |
| 订阅 | `u` `n` `x` `c` `y` | 更新 / 新增 / 删除（确认）/ 编辑定时刷新 cron（表达式+开关）/ 复制订阅链接 |
| 订阅 | `e` | 编辑订阅标签与链接（`tagSubscription`/`updateSubscriptionLink`，保留订阅 ID；改链接不重新拉节点） |
| 订阅 | — | 左栏对失败订阅直接显示 `info` 首行原因（不再只给一个 status 字符串）；节点列表按订阅缓存，改标签/cron 不会触发全量重拉，只有 `u` 更新才失效重取 |
| 配置 | `Enter` | 切换选中项（应用用全局 `A`）。注意：daed 的 run(dry) 是"停止代理"而非校验，已移除该键 |
| 配置 | `c` `R` `D` | 新建（克隆当前选中配置）/ 重命名 / 删除（确认；选中的与最后一个条目受保护） |
| 配置 | `e` | config 逐字段编辑（字段清单来自 `configFlatDesc`，含类型/默认值/说明，覆盖全部 global 字段；输入按类型预校验——int/bool/duration/数组，lan/wan 接口字段会列出本机网卡并标出不存在者）；DNS/路由默认调 `$EDITOR` 编辑 DSL 原文（config.toml 里 `editor = "builtin"` 换成内置浮窗编辑器：textarea 直接改，`ctrl+s` 校验，语法错误原位提示并保持打开），退出/提交后先后端语法校验、再展示 diff，`y` 确认才提交（取消则 $EDITOR 的临时文件保留，内置编辑器则退回编辑器） |
| 配置 | `v` | DNS/路由右栏切换：DSL 原文 ↔ 解析后的结构概览（规则清单，默认显示原文）。路由方案还会列出它引用的名字（`referenceGroups`）：群组消失的标红，dae 内置方向（direct 等）标"内置" |
| 配置 | `y` | 复制当前方案 DSL 到剪贴板（OSC 52） |
| 配置 | `Tab` 后 `j/k` | 滚动查看右栏内容（config 字段摘要 / DNS、路由 DSL 原文） |
| 节点列表 | `/` | 过滤框：按 名称/协议/标签/地址 子串实时过滤（不区分大小写），标题显示 `命中/总数` |
| 节点列表 | `enter` / `esc` | 关闭过滤框并保留结果 / 关闭并清空 |
| 节点列表 | `o` | 循环排序：默认顺序 → 延迟↑ → 延迟↓（未测/死亡节点始终排最后） |
| 节点列表 | `t` | 测速只测当前可见的节点（过滤后剩下几个就测几个） |

“节点列表”指群组页右栏的成员节点、`n` 添加节点选择器、订阅页右栏节点、手动节点页
左栏列表——四处的过滤与排序行为基本一致（过滤状态下群组页会自动展开分区，无命中的
分区直接隐藏）。唯一差别：群组页的排序在**订阅/直接两个分区内部**各自生效，分区结构
和分组语义不变。三处节点行的延迟列为**微型条 + 毫秒值**（同色阶，500ms 打满；窄于
56 列的面板自动只留毫秒值），死亡节点显示"超时"。

## 架构

```
cmd/dae-tui          入口（flag 解析、-probe 自检、-cmd status/test 子命令）
internal/driver/     后端抽象：Driver 接口 + 领域类型（Group/Node/Latency/Traffic…）
internal/driver/daed daed GraphQL 驱动（client/auth/queries/types/driver）
                     schema.graphql = daed v2.1.1 最终版 SDL 的固化副本
internal/app/        Bubble Tea 应用：根模型（连接→登录→主界面）、各页面
                     （home/groups/subs/nodes/configs/login/help）
                     横切工具：nodelist.go（节点过滤/排序）、lathist.go（延迟历史）、
                     clip.go（OSC 52 剪贴板）、diff.go（DSL 编辑 diff）
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
  （驱动保留该能力，但 UI 不再创建 fixed 组，原因见"已知限制"。）
- 配置字段元数据：`configFlatDesc` 列出 config.dae 的全部字段；`mapping` 是扁平键
  （`global.tproxy_port`），与 GraphQL `globalInput` 的 camelCase 键按 snake→camel 对应，
  udphop 之类连写词也成立（`global.udphop_interval` → `udphopInterval`）。字段清单由它
  驱动，不写死在代码里。
- DSL 预校验：`parsedRouting(raw)` / `parsedDns(raw)` 只解析不落盘；语法错误以 GraphQL
  error 返回（带行列号和插入符行）。$EDITOR 保存后先过这一关再提交。解析结果同时用于
  配置页的"结构概览"视图（`v` 切换），规则渲染成 DSL 形态——注意 daed 把 `must_direct`
  解析为 `direct` + 位置参数 `must`，渲染时要拼回 `must_direct`。
- 流量：轮询 `general { runtimeOverview(windowSec, maxPoints) }`；注意
  `uploadTotal/downloadTotal` 在 SDL 中是 String。
- 测速：`testNodeLatencies(ids)` 触发，`nodeLatencies(ids)` 读回（以 `testedAt` 变新为准）。
- 原地编辑（保留 ID，群组/订阅挂载不丢）：`tagNode` / `updateNode`（手动节点）、
  `tagSubscription` / `updateSubscriptionLink`（订阅，改链接不重新拉节点）。对应的
  "删了重导" 会拿到新 ID，`groupAddNodes`/`groupAddSubscriptions` 按 ID 绑定，成员关系
  会静默丢失。
- 批量导入：`importNodes(rollbackError: false, args: [...])`——rollbackError 必须是
  false，否则一条坏链接让整批回滚；返回 `[NodeImportResult!]!`（`link`/`error`/`node`）
  正好逐条渲染成 "3/10 成功"。
- 路由引用：`routings { referenceGroups }` 给出每个路由方案按名字引用的群组。改名/删组
  daed 不报错，只是规则静默失效——UI 据此在 `R`/`D` 确认框和配置页告警。
- 网卡：`general { interfaces { name ip flag { up default { gateway } } } }`，
  lanInterface/wanInterface 按名字绑定，DHCP 改名后代理会静默不通——首页网络块与配置页
  字段提示都靠它。
- 账户：`updatePassword(currentPassword, newPassword)` 返回新 token（旧 token 随即失效），
  驱动会把新 token 与新密码都落盘（静默续期靠保存的密码）；`Logout` 只是清本地会话，
  daed 没有 logout mutation。
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

**暂不提供"固定到节点"**：daed v2 的 fixed 组只允许一个成员，固定某个节点必须摘掉组内
全部订阅挂载与其他节点、只保留该节点并设 `fixed(0)`——整组被静默重组，且无法一键恢复。
在 daed 给出更合理的语义（或有优雅的交互方案）之前不提供该入口；**已有的 fixed 组仍
然只读展示**（首页/群组页显示当前固定节点），用 `p`/`a` 可随时切回自动策略。

`parsedRouting`/`parsedDns` 只做**语法**校验：引用了不存在的组名要等 `A` 应用时才暴露。
为此 UI 在两边补齐了提示——群组页 `R`/`D` 用 `routings { referenceGroups }` 点名会受
影响的方案，配置页路由右栏直接列出引用组（已消失的标红）。

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

## 已知限制

1. **没有"固定节点"入口**：daed v2 的 fixed 组只允许一个成员，切固定节点需要摘掉全部
   订阅挂载 → 移除其他直接节点 → 确保目标节点是直接成员 → `fixed(0)` 的一串 mutation，
   整组被静默重组且无法一键恢复。驱动保留该能力（`driver.Policy.FixedIndex`），UI 只读
   展示已有 fixed 组；要恢复需按同样顺序重放 mutation。因此 CLI 也没有 `-cmd switch`。
   切某组的节点请走：首页 `Tab` → 选中组 → `Enter` 跳到群组页并展开。待 daed 官方对
   fixed 组给出合理方案后再适配。
2. **延迟数据按页轮询**：每 3 秒只轮询当前页可见节点的 `nodeLatencies`（群组页=展开
   分区的节点行/选择器候选，订阅页=右栏可见节点，手动节点页=列表；首页和配置页不轮询）。
   启动时也不再触发全节点测速——测量由 `t`/`T` 按需创建。代价：非当前页的延迟列停在最后
   一次的值上；首页自动策略组的 `≈` 最优节点只用已测数据、不显示毫秒数，没有测量数据时
   标注「未测速」（要精确到每个节点的延迟，去群组页展开对应组）。
3. `L` 日志视图是本地 `journalctl`，SSH 隧道管理的远程 daed 用不了。

## 兼容性说明

daed 与其后端 dae-wing 已于 2026-09-24 归档，v2.1.1 为最终版本（bundle dae v2.1.x）。
其 GraphQL API 从此冻结：好处是永远不会破坏变更，坏处是上游不再有安全修复。本工具按
v2.1.1 schema 开发（SDL 已固化在仓库中做回归对照）。dae 本体仍在活跃维护；若未来迁移
到裸 dae，见上文 Phase 2 计划。
