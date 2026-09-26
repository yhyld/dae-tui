# AGENTS.md

dae-tui 的仓库说明，供后续 ZCode agent 快速上手。细节以 `README.md` 为准（本文只写
不看代码就会踩的点）。

## 项目是什么

`dae` eBPF 代理的终端管理界面（TUI），**当前唯一后端是 daed 的 GraphQL API**（daed
v2.1.1，上游已于 2026-09-24 归档，schema 从此冻结）。功能：切换路由组节点、测速、
实时流量、订阅管理、config/DNS/routing 方案切换与应用。

用户可见文案全部是**中文**（UI 字符串、toast、错误提示）；Go 文档注释用英文。

## 常用命令

```bash
go build -o dae-tui ./cmd/dae-tui   # 构建（产物 /dae-tui 已被 .gitignore）
go test ./...                       # 全部测试：driver httptest mock + TUI 无头渲染冒烟
go vet ./...
go run ./cmd/dae-tui -probe         # 非交互自检，只读打印后端状态
go run ./cmd/dae-tui -cmd status    # 非交互一行式状态（可绑快捷键/写脚本）
go run ./cmd/dae-tui -cmd test -g 组名   # 非交互测速（-n id,id 指定节点；不给则全部）
```

Go 1.27.1（go.mod 写 1.27.1）。直接依赖仅 5 个：bubbletea / bubbles / lipgloss /
BurntSushi-toml / charmbracelet/x/ansi。**不要引入新依赖**，除非确有必要。

联调（无副作用）：`daed run --api-only -l 127.0.0.1:2024 -c /tmp/daed-dev` 起一个无
eBPF 的纯 API 实例，再用 `-endpoint http://127.0.0.1:2024/graphql` 指向它。注意
`--api-only` 实例没有 dae 核心，`testNodeLatencies`/`nodeLatencies` 会返回
"record not found"——测速只能在真实运行的 daed 上验证。

## 目录与分层

```
cmd/dae-tui/main.go   入口：flag 解析、config 加载、driver 装配、probe 与 -cmd 子命令
internal/driver/      Driver 接口 + 领域类型 + Caps 能力位（**不含任何 daed 细节**）
internal/driver/daed/ daed GraphQL 驱动（client/auth/queries/types/driver + schema.graphql）
internal/app/         Bubble Tea 应用：根 model + 各页面（home/groups/subs/nodes/configs/login/help）
                      横切工具：nodelist.go（节点过滤/排序）、lathist.go（延迟历史）、
                      clip.go（OSC 52 剪贴板）、diff.go（DSL 行 diff）
internal/ui/          lipgloss 样式、延迟色阶、CJK 宽度工具、braille sparkline
internal/config/      ~/.config/dae-tui/config.toml（0600）
```

**分层铁律：**

- UI（`internal/app`）只依赖 `driver.Driver` 接口，**禁止 import `internal/driver/daed`**；
  daed 驱动只在 `cmd/dae-tui/main.go` 装配。新增后端 = 在 `internal/driver/<name>/` 下
  新包实现 `driver.Driver`，不改 UI。
- `internal/driver` 里的类型是归一化领域模型，不要为了迁就某个后端往里加特例字段；
  后端差异用 `Caps` 能力位让 UI 降级。
- GraphQL 文档是 `queries.go` 里的手写字符串常量（`q*` 查询 / `m*` mutation），响应
  结构体在 `types.go` 手写、json tag 对齐 `schema.graphql`。**不用 codegen。**

## daed API 坑（改驱动前必读）

- 认证失败是 **HTTP 200 内的 GraphQL error `"access denied"`**，不是 401。`Client.Do`
  检测到后自动跑一次 re-auth（用保存的凭据重取 30 天 JWT）并重放，仍失败才返回
  `driver.ErrNeedAuth`。
- `uploadTotal`/`downloadTotal` 在 SDL 里是 **String**，要 `parseInt64`。
- `Group.Nodes` **只含直接挂载的节点**；完整成员列表用 `Group.Members()`（订阅贡献的
  节点 + 直接节点，按 ID 去重）。
- 切固定节点：`groupSetPolicy(id, policy: fixed, policyParams: [{val: "<index>"}])`，
  **key 留空**才会渲染成 dae DSL 的位置参数 `fixed(<index>)`。index 相对
  `group.nodes` 顺序。**UI 已移除"固定节点"入口**（见下），但驱动仍保留该能力，
  `driver.Policy.FixedIndex` 与 `Group.FixedIndex()/SelectedNode()` 仍在（只读展示
  已有 fixed 组）。
- daed v2 的 fixed 组只允许一个成员，"固定节点"因此是摘掉所有订阅挂载 → 移除其他
  直接节点 → 确保目标节点是直接成员 → `fixed(0)` 的一串操作：整组被静默重组且无法
  一键恢复。这就是 UI 去掉该功能的原因；要恢复需按同样顺序重放 mutation（历史上在
  `msgs.go` 的 `pinNodeCmd`，已删除，git 历史可查）。
- `configFlatDesc` 给出 config.dae 全部字段的元数据；`mapping`（如
  `global.tproxy_port`）与 GraphQL `globalInput` 的 camelCase 键按 snake→camel 对应
  （驱动里是 `globalInputKey`）。可编辑字段清单由它 + `configs { global }` 实际返回的
  键交集决定，**不要**再把字段列表写死在代码里；`configFlatDesc` 查询失败时降级为
  按返回键推断类型。
- `parsedRouting(raw)` / `parsedDns(raw)` 只解析不落盘，语法错误是带行列号的 GraphQL
  error。`$EDITOR` 改完 DSL 后先过 `ValidateRouting`/`ValidateDns` 再提交；校验失败时
  **临时文件必须留着**（用户靠它恢复编辑），成功才删。解析结果也喂配置页的"结构概览"
  视图（`v` 与 DSL 原文互斥显示，别又把两者上下叠一起）。
- DSL 多条件的连接符是 `&&`（`dip(1.2.3.4) && domain(x) -> proxy`），不是逗号；键值参数
  写法是 `geoip:private`（无空格）。`must_direct` 在解析结果里是 `direct` + 位置参数
  `must`，渲染概览时要拼回 `must_direct`。
- **路由预设**（首页快速切换，`internal/driver/daed/presets.go`）：模板照搬 daed web UI
  的 simple mode，4 个（gfw/nonCn/cnOnly/global）都以同一段前缀开头
  （`pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct` +
  `dip(geoip:private) -> direct`）。组名是直接插值进 DSL 的，**必须先过
  `validGroupName`**，否则生成出的 DSL 后端解析不了。`DetectRoutingPreset` 要求前缀
  存在且每一行都是预设自身能产生的规则才识别——daed 自带默认模板的 pname 参数与我们的
  不同（没有 dnsmasq），所以前缀按语义匹配而不是整行比对。改模板要同步改检测，两边都有
  单测兜着（生成的 DSL 必须 round-trip 回同一预设）。
- `run(dry: true)` 是**停止代理**，不是校验。别把它当 dry-run 用。
- 节点列表是 connection，cursor 就是节点 ID；驱动内循环翻页（200/页，带防御性页数上限）。
- `testNodeLatencies` 的 ID 列表超过 100 个要分块，否则 HTTP 超时。
- 老版本 daed 没有 `GroupSubscription` 类型：`qGroupsRich` 会 schema 校验失败，驱动据此
  永久降级到 `qGroups`（`groupsFallback`）。
- **routing 按名字引用组**：`routings { referenceGroups }` 给出每个路由方案引用的名字。
  改名/删组 daed 不报错、只是规则静默失效，所以群组页 `R`/`D` 确认框（`refNote`）和配置页
  路由右栏都要点名。注意 referenceGroups **把 dae 内置 outbound（direct/must_direct/block/
  must_proxy）也列进来**——它们不需要群组存在，配置页渲染时用 `isBuiltinOutbound` 区分
  （标"内置"而不是"已不存在"）；群组页只在用户真有同名组时才告警，方向保守。组名是 DSL
  标识符，检测/生成都别假设它能安全插值。
- **原地编辑保留 ID**：`tagNode`/`updateNode`（手动节点）、`tagSubscription`/
  `updateSubscriptionLink`（订阅）。`groupAddNodes`/`groupAddSubscriptions` 按 ID 绑定，
  所以"删了重导"会静默丢掉群组挂载——这就是 UI 提供编辑入口的原因。`updateSubscriptionLink`
  不重新拉节点（要重新拉用 `updateSubscription`）。
- **批量导入**：`importNodes(rollbackError: false, args: [...])`，返回
  `[NodeImportResult!]!`（link/error/node）。rollbackError 必须 false——批量的意义就是
  "能进的进，不能进的报出来"；坏链接的 error 里可能有原始字节，驱动侧用 `printable` 过滤。
- **网卡**：`general { interfaces { name ip(onlyGlobalScope: true) flag { up default { gateway } } } }`。
  `lanInterface`/`wanInterface` 按名字绑定，DHCP 改名后代理静默不通；首页网络块与配置页
  字段提示（`ifaceHint`/`ifaceWarning`）都靠它。查询失败（老 daed 无此字段）降级为空列表，
  不要报错打断页面。**`auto` 是 dae 的“自动探测”占位值（daed 默认 wan_interface 就是它），
  不是网卡名**——拆分接口字段值一律走 `configuredIfaces`，它负责把 `auto` 剔掉，别直接
  `FieldsFunc` 拆分后当网卡名去比对。
- **账户**：`updatePassword(currentPassword, newPassword)` 返回新 token 且旧 token 随即
  失效；驱动必须把新 token 和新密码都落盘（静默续期重放的是保存的密码）。daed 没有 logout
  mutation，`Logout` 只清本地会话（opts + client token）。

## TUI 约定

- **整个应用包在一个圆角外框里，恰好填满终端**：`Model.View()` 把 status/tabs/body/
  toast/help 拼好后套 `RoundedBorder`（宽 `width-2` 高 `height-2`），帮助条永远钉在
  最后一行。`layout()` 的 chrome 数学 = 外框 2 行 + status/tabs/toast/help 4 行，
  页面内容高度是 `height-6`、宽度 `width-2`——改 chrome 行数要同步改这两处与
  `View` 里的钳制。终端小于 60×12（`minTermW/minTermH`）时渲染 `smallView` 降级页，
  别让两栏数学溢出换行。
- **chrome 行（status/tabs/toast/help）与 body 每行都必须过 `ui.Truncate` 到内容宽**：
  高度数学假设它们各占一行，lipgloss 的 `Width()` 会把超宽行折行，折一行就挤掉一个
  chrome 行。`ui.Pane` 也按宽度截断每行、按高度补齐空行（左栏 `bar=false`——外框就是
  它的边缘；右栏 `bar=true`——那根竖线兼作两栏分隔线，聚焦时变 `▌`）。
- **一次性动作反馈走 `opDoneMsg` toast**（根模型 4 秒自动消失），不要写进 `pickErr` 这类
  常驻面板字段——它会留到重启才消失，看起来像坏了的状态。`pickErr` 只留给"选择器打开
  期间拉取失败"这种与当前模态绑定的错误，并在 groups/subs 刷新时清空。
- **浮窗与面板的分工——决策和填表用浮窗，浏览和对比用面板**。各页实现 `overlay() *overlaySpec`
  （表单/对话框），根模型 `pageOverlay()` 收集（账户窗口优先，`P` 已是全局键，任何页面可开），
  `ui.Overlay` 负责 ANSI 感知地居中叠到页面画面上（`Truncate` 取左 + 补 reset、`TruncateLeft`
  取右——注意 TruncateLeft 会丢弃被跳过区域的转义序列，盒子右侧窄条可能掉色，文字不受影响）。
  浮窗清单：帮助（`helpOpen`，`?` 任何页面）、账户、全局 `A` 确认、首页预设确认+DSL 预览、
  各页输入表单（含内置 DSL 编辑器 mode 7）。留在右栏的：小 y/n 确认（删组/删节点/删订阅/
  删配置、组内移除）、大列表选择器（加节点、字段选择）、DSL diff 确认。
- **内置 DSL 编辑器**（configs mode 7，`config.toml` 的 `editor = "builtin"` 开启）：
  textarea 浮窗，`ctrl+s` → `validateTextCmd(path="")` → `editorValidatedMsg`；校验失败时
  `handleValidated` 检测 `mode==7` 把错误写进 `edErr` 并**保持编辑器打开**；成功进 mode 6，
  `diffState.Builtin` 标记来源，diff 里按 `n`/esc 回到编辑器（内容不丢），按 `y` 直接提交。
  $EDITOR 路径（默认）不变：临时文件在"应用后/内容未变"才删。
- **浮窗期间按键归属**：`helpOpen` 与 `home.acct != 0` 计入 `anyModal()`，且两者的按键在
  根模型 `handleKey` 顶部优先分发（账号窗口在任何页面都能开，esc 不能被所在页吃掉）；
  鼠标滚轮在 helpOpen 时滚帮助、其余模态期间忽略。
- 模态渲染在右栏内部的（上面的"留在右栏"清单）用 `ui.BoxLines(destructive, lines...)`
  （红框=破坏性），列表型选择器保持裸行；不能 `body += "\n" + box` 追加在双栏下方——
  面板补齐到满高，下方追加的内容第一个被硬钳制裁掉。
- 首页有跟随滚动：`bodyLines()` 返回行列表+活动行，`View` 让窗口跟随活动行（`follow`
  在任何按键后重新启用；滚轮 `scrollBy` 暂时关闭跟随）。帮助是浮窗，内容用 `helpScroll`
  （j/k/G）在盒内滚动，`clampHelpScroll` 的窗口数来自 `helpWinBody`。
- **鼠标已启用**（`tea.WithMouseCellMotion`）：滚轮 = 3×j/k（帮助浮窗/首页直接滚偏移），
  点页签切页（`tabClick` 按渲染宽度算 span），点左栏行选中（各页 `leftClick` 复算
  `leftLines` 的窗口偏移），点底部键位行任意位置打开帮助浮窗（呼出键 `? 帮助` 固定在
  该行右端且**不参与截断**——`helpLine` 先截键位再拼提示，页签栏不要放帮助标识）。`anyModal()` 时鼠标全部忽略——弹窗期间误点比不点更糟。
  鼠标 handler 是**值接收者**（与 `handleKey` 一致），别改成指针接收者，否则
  `tea.Model` 的动态类型在键盘/鼠标两条路径上不一致。
- 每个内容页都是**左列表 + 右详情**双栏：左栏 `j/k` 移动（默认折叠），`Tab/l/Enter`
  展开到右栏（右栏有自己的高度与滚动），`h/esc` 收起。右栏内容用 `ui.Pane` 渲染。
- **节点列表的过滤/排序统一走 `internal/app/nodelist.go` 的 `nodeView`**（`/` 开过滤框，
  `enter` 保留、`esc` 清空，`o` 循环排序，未测/死亡节点排序时殿后）。群组页右栏、`n`
  选择器、订阅页右栏、手动节点页四处都内嵌它；新增节点列表必须复用，不要另写一套。
  过滤框打开时所有按键归它（并计入 `anyModal()`），`t` 测速只测可见节点。群组页在过滤
  状态下要把分区视为展开、无命中分区直接不建行（见 `rebuild`），并且换组时必须
  `rebuild()`——否则右栏会残留上一个组的行。
- 所有 IO 走 `tea.Cmd`（`msgs.go` 的 `withCtx`/`withCtxT`，默认 12s 超时），
  **`Update` 永不阻塞**。mutation 成功后由同一个 cmd 顺带重新拉列表，返回
  `groupsMsg`/`subsMsg`/`selectionsMsg` 等。
- 改节点/订阅用**原地编辑**（`e`，`tagNode`/`updateNode`、`tagSubscription`/
  `updateSubscriptionLink`）：ID 不变，群组挂载才不丢。删除 + 重新导入看起来等价，
  实际会静默断掉 `groupAddNodes`/`groupAddSubscriptions` 的 ID 绑定——别为省一个表单
  把它改回"删了重导"。
- 批量导入框（手动节点页 `a`）是 bubbles 的 **textarea**（每行一条链接；ctrl+s 或切到
  标签框后 enter 提交，框内 enter 是换行）。导入结果走 `importDoneMsg`：成功数进 toast，
  失败明细渲染在右栏（`importFail`）——一行 toast 说不完逐条报错。
- 首页 `P` 是账户状态机（`homePage.acct`：0 无 / 1 菜单 / 2 改密码 / 3 退出确认），
  已计入 `anyModal()`；退出登录由根模型处理（`logoutMsg`：清 cfg + 存盘 + `drv.Logout` +
  回 `phaseLogin`），页面自己不能改 phase。首页 `L` 是 daed 日志视图（`tea.ExecProcess`
  跑 `journalctl -u daed -n 200 --no-pager -f`，只读、仅本机有效），退出即回到 TUI。
- 全局 `r` 是**全量刷新**：所有列表 + status + traffic + 当前页延迟轮询一次性重拉，
  不是只刷当前页——各页共享组/订阅/方案数据（首页显示组、群组页显示订阅标签、首页
  路由区来自 selections），只刷当前页会让切过去后的视图是旧的。
- **`Model.View()` 末尾的硬钳制不能删**：body 行数超过 `height-6`（外框 2 行 + chrome
  4 行）就截断、不足就补齐空行，否则页签/帮助条会被挤出屏幕（这是最早修的滚动 bug；
  钳制现在同时负责"撑满终端+页脚贴底"）。
- 弹窗/输入框打开时（`anyModal()`）所有按键——包括 `1`-`5` 翻页热键——必须进弹窗，
  因为名称、cron 表达式、分享链接里全是数字。
- 宽度计算一律走 `ui.PadRight`/`ui.PadLeft`/`ui.Truncate`（内部走
  `internal/ui/width.go` 的 `ansi.StringWidth`，与 `lipgloss.Width` 同一口径），
  中文/国旗/emoji 都占两列，别用 `fmt` 的 `%*s`。不要直接引 `runewidth`：
  它的 `StringWidth` 取每个 grapheme 簇第一个非零宽 rune 的宽度，会把国旗对和
  "emoji+变体选择符" 少算一列，导致后一列被盖掉。
- 节点名（`Node.Name`）在**任何**展示点都要过 `ui.SpaceAfterFlag`：订阅节点名常是
  `🇩🇪Germany 01` 这种旗贴字格式，国旗占两列且字形顶到国家名上，看起来像重叠。
  列表、详情、标题、toast、确认框一处都不能漏。
- 延迟色阶：未测/死亡 = 灰，<200ms 绿，<500ms 黄，其余红；节点行的延迟列统一走
  `latencyCell`（`nodelist.go`）：≥56 列的面板在毫秒值前多一条 5 格微型条
  （`ui.LatencyBar`，500ms 打满，与数值同色）——窄面板（如 36 列的左栏）自动去掉条只留
  数值，别在窄栏里硬塞。自动策略下的"当前节点"是
  估算值，显示时加 `≈` 前缀（没有测量数据时首页标注「未测速」；首页不显示毫秒数，
  逐节点延迟去群组页看）。
- 破坏性操作（删组/删节点/删配置/停止代理/应用配置）都要 `y` 确认。

## 横切机制（改 UI 前必读）

- **Caps 降级已接线**：`New()` 启动时读一次 `drv.Capabilities()` 存进 `Model.caps` 并传给
  各页面。按键按能力位门控，不支持时 `unsupportedCmd(op)` 出 toast（"当前后端不支持
  该操作"），订阅/配置页还有横幅；`!TrafficStats` 时首页不渲染流量图、轮询也跳过。
  新 driver 必须如实返回 Caps——`stubDriver` 返回全 true，写"残废后端"测试要内嵌它再覆盖。
- **测速进度**：`testWindow(n) = 15s + 200ms×n`（上限 2 分钟，与 `testLatencyCmd` 的
  ctx 超时一致），三个页面各自 `testProgress()`，根模型 `tabsBar` 显示盲文 spinner +
  "测速中 x/y"——进度是**跨页汇总**的（测速中切页指示不消失）。spinner 由 120ms 的
  `spinnerMsg` 自续链驱动（`spinning` 防止叠链；1s tick 在测速开始时拉起链，无测速时链
  自灭，空闲 UI 不空转）。
  完成判定仍是"所有 testIDs 的 `TestedAt` 都新于 baseline"。测速期间的 `testIDs` 轮询
  （每秒）与下面的按页轮询并存，互不影响。
- **延迟按页轮询，无启动全量测速**：每 3 秒只轮询当前页可见节点的 `nodeLatencies`
  （`Model.visibleLatencyIDs()` 按页分发：群组页=展开分区的节点行，选择器打开时=候选；
  订阅页=右栏可见节点；手动节点页=列表；首页/配置页不轮询）。`initialLoad` **不再**
  触发 `testLatencyCmd(nil)`——启动即全节点测速是最大的一次性后端负载，且和"只维护
  可见列表"矛盾；测量改由 `t`/`T` 按需创建。连带影响（写进 README 已知限制）：非当前页
  延迟列停在最后值；首页自动策略组的 `≈` 只用已测数据且**不显示毫秒**（`currentNode`
  为此去掉了 `(%dms)`——要全组成员的延迟就得全量轮询，正是要避免的）；`latHistory` 只
  累积被轮询到的节点，LRU 256 兜底。测速中的进度轮询（`testIDs`）不变。
- **延迟历史**（`lathist.go`）：`latHistory` 按节点保留最近 60 个采样（约 3 分钟），
  LRU 上限 256 个节点；根模型在 `latenciesMsg` 里喂数据，手动节点页详情页画 sparkline。
  死亡采样占窗口位但不绘制（`series()` 只收 alive>0）。历史只来自轮询流，没有独立采样器。
- **OSC 52 剪贴板**（`clip.go`）：`osc52CopyCmd` 只在 stdout 是字符设备时写转义序列
  （测试/管道环境下返回 `clipboardMsg{OK:false}`，根模型转 toast）。订阅/手动节点/配置页
  的 `y` 键。没有引入任何剪贴板依赖。
- **群组页多选**：`space` 按**节点 ID**标记（`marked map[string]bool`），`t` 只测已标记、
  `x` 批量移除已标记的**直接挂载**节点（订阅贡献的只能随订阅移除，会给 toast 说明）、
  选择器内 `Enter` 批量添加。`markedIDs()/markedDirectNodes()` 必须去重——同一节点可以
  同时出现在订阅区和直接区两行。换组/收起（`collapseSections`）清空标记。
- **首页两栏**：内容宽 ≥96 列（`homeTwoColMin`）时流量块与路由预设并排
  （`homeRoutingW=44`），窄于此保持上下堆叠；`bodyLines` 里手工逐行拼（左栏 PadRight 到
  leftW 再接右栏），预设行的 active 索引要算上列偏移，改动时注意 `presetStart` 的换算。
- **首页组行跳转**：`Tab` 在路由选择器与组列表间切焦点（`home.groupFocus`），组行
  `Enter` 发 `gotoGroupMsg{ID}`，根模型开群组页并展开该组（`expanded/focus=1`）。
  焦点在组列表时只吞导航键，`o/P/L/g` 等仍走原路径。
- **订阅页节点缓存**：`subsMsg` **不再**清空 `subNodes`；只有 `u`（更新）把对应 ID 记入
  `stale`，下次 `handleSubs` 时删那一条（并顺带清理已删除订阅的残留），根模型随后
  `ensureNodes` 重取。`ensureNodes` 自带 `expanded` 判断，任何地方调用都安全。
- **群组页分区展开按订阅 ID 键定**（`subOpen map[string]bool`）：刷新后订阅重排不会
  把展开状态错位到别的订阅。`directOpen` 是整个直接区的单个 bool。
- **DSL 编辑流程**：`$EDITOR` 退出 → 后端校验 → **diff 确认**（`configsPage.diff`，
  mode 6，`y` 提交/`n` 取消）→ 才 `configTextCmd`。临时文件只在"应用后"和"内容未变"
  删除；校验失败和用户取消都保留并在 toast/右栏说明路径。diff 用 `diff.go` 的 LCS，
  长相同行折叠成 gap 标记。
- **配置页左栏分区标题是光标行**（`rowRef.kind = rowHeader`）：光标会停在标题上，
  `item()` 对标题行解析为该分区"当前选中（否则第一个）"的条目，所以标题上的
  `Enter/e/R/D` 作用在用户上次选的方案上。空分区不建行。
- **字段输入预校验**（`fields.go: validateFieldValue`）：按 `ConfigField.Type`
  （int/bool/duration/array）在客户端挡掉明显非法的值，错误显示在输入框模态内，
  不提交。语义仍然归后端。
- **`tea.ExecProcess` 出来必须补 `reenableMouse()`**（`app.go`）：bubbletea v1.3 的
  ReleaseTerminal 会关掉鼠标上报，RestoreTerminal 只恢复 altscreen/括号粘贴/焦点上报、
  **不恢复鼠标**——不加这条，$EDITOR 或日志视图退出后点击/滚轮全部静默失效。目前挂在
  `editorDoneMsg` 和 `logsDoneMsg` 两个分支，新增 ExecProcess 调用点要同样处理。
- **日志视图**：首页 `L` 用 `tea.ExecProcess` 跑 `journalctl -u daed -n 200 --no-pager -f`。
  本地 exec，远程隧道场景天然不可用（LookPath 失败时 toast 说明）。测试里**不要**执行
  这个 cmd——它会一直 follow 不退出。
- **CLI 子命令**（`main.go`）：`-cmd status|test`，`test` 支持 `-g 组名` / `-n id,id`。
  刻意没有 `-cmd switch`：fixed 组改组是破坏性操作，理由见"已知限制"。

## 测试约定

- driver 层用 `httptest.Server` mock GraphQL，覆盖 auth 流程、access-denied 自动重试、
  分页拉全、mutation 请求体构造、String 型 totals 解析。
- app 层用 `stubDriver`（内嵌 `driver.Driver` 接口以便只覆盖需要的方法）喂罐头数据，
  做**无头渲染冒烟**：驱动 `Update` + `View`，断言关键文案出现/不出现，并断言按键
  是否产生了 `tea.Cmd`。改 UI 先跑 `go test ./internal/app`。
- 断言用的是中文字面量（如 `"确认删除群组"`），改文案会破坏测试，两边一起改。
- 观察"驱动被问了什么"用包级记录器：`lastTestIDs`（TestLatency 的 ids）、
  `lastAddNodeIDs`（AddGroupNodes 的 ids）、`lastLatencyIDs`（Latencies 的 ids 参数）。
  要检查一批 cmd 里到底发了哪些请求：`cmd().(tea.BatchMsg)` 后逐个执行、按消息类型
  断言（见 `TestNoStartupLatencyTest` / `TestForceRefreshReloadsEverything`）。
  执行 `tea.ExecProcess` 的 cmd（首页 `L`）**不能**在测试里跑——journalctl -f 不会退出。
  一个 cmd 可能是 `tea.Batch`（如 editorDoneMsg 顺带 reenableMouse）：用 `execCmds` 展开、
  `firstMsgOf[T]` 取目标消息，别对 `cmd()` 直接做单类型断言。

## 安全与兼容

- `internal/config` 的 config.toml 含密码和长期 JWT，写死 0600；**不要把凭据/token
  打进日志或错误信息**。
- daed 默认监听 `0.0.0.0:2023` 纯 HTTP；本工具只走回环 + SSH 隧道，不要改这个前提。
- daed 已归档、schema 冻结：这是特性不是 bug——永远不会有破坏性变更，但也不会有安全
  修复。`schema.graphql` 是固化的 SDL 副本，用于回归对照，不参与编译。

## 其他

- 仓库已有 commit（`git log` 可查）。除用户明确要求，不要 `git commit`——改动留在工作区
  即可，由用户自己决定何时提交。
- `.zcode/plans/` 下是最初的实施计划，可当历史背景读，但目录结构已与实际代码有偏差
  （实际没有 `app/pages/`、`app/keys.go`、`auth.go`），以代码为准。
- 未来方向（接口已预留，未实现）：裸 dae 驱动（Phase 2）、clash-api 驱动。动
  `driver.Driver` 接口时保持向后兼容的加法式演进。
