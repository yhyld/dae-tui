# AGENTS.md

dae-tui 的仓库说明，供后续 ZCode agent 快速上手。细节以 `README.md` 为准（本文只写
不看代码就会踩的点）。

## 项目是什么

`dae` eBPF 代理的终端管理界面（TUI），**当前唯一后端是 daed 的 GraphQL API**（daed
v2.1.1，上游已于 2026-09-24 归档，schema 从此冻结）。功能：切换路由组节点、测速、
实时流量、订阅管理、config/DNS/routing 方案切换与重载。

用户可见文案默认**中文**，可切英文（`internal/i18n`）；Go 文档注释用英文。

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
  检测到后自动跑一次 re-auth（用保存的凭据重取 30 天 JWT）并重放；re-auth 是
  **singleflight** 的——并发请求同时在飞时共享同一次刷新、等它完成后重放，而不是
  各自报错。re-auth 失败或重放仍被拒都返回 `driver.ErrNeedAuth`（app 根模型对所有
  带 Err 的结果消息做 `errors.Is` 检查，会话中途遇到就直接回 `phaseLogin`，不会
  toast 刷屏）。re-auth hook 自己的请求带 `reauthCtxKey` 标记，防止在刷新 leader
  身上自我死等。`d.opts` 凭据字段由 `optsMu` 保护（Login/改密/退出在写、任意请求
  goroutine 的 re-auth 在读）。两条新铁律：**follower 等刷新时自己的 ctx 过期也要包
  `ErrNeedAuth`**（裸 "access denied" 会漏过 `errors.Is`，测试 `TestFollowerExpiry
  ReportsErrNeedAuth` 兜底）；**登出带会话代数**——`Logout` 递增 `d.session`，re-auth
  完成后复查代数，旧会话拿到的新 token 直接丢弃（否则在途刷新会把已登出的 token
  写回内存和 config.toml，"退出登录"最长 30 天不彻底，`TestLogoutVoidsInFlightReAuth`
  兜底）。错误包装一律 `%w` 不断链。
- `uploadTotal`/`downloadTotal` 在 SDL 里是 **String**，解析走 `parseInt64Strict`：
  空串按良性 0，其余解析失败**上抛错误**而不是静默归零（累计流量悄悄清零读起来像
  后端重启）。
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
  存在且**规则集与某预设的产出严格相等**（逐类计数：多一条重复/额外规则、两条
  fallback、第二个 pname 行都算自定义）——daed 自带默认模板的 pname 参数与我们的
  不同（没有 dnsmasq），所以前缀按语义匹配而不是整行比对。改模板要同步改检测，两边都有
  单测兜着（生成的 DSL 必须 round-trip 回同一预设，超集/重复规则必须判自定义）。
- `run(dry: true)` 是**停止代理**，不是校验。别把它当 dry-run 用。
- 节点列表是 connection，cursor 就是节点 ID；驱动内循环翻页（200/页，带防御性页数
  上限 `nodePageCeiling`=100 页、两处一致——它翻的是全量节点
  connection 再客户端过滤，上限不一致会在大实例上静默丢手动节点；触顶返回已有节点
  不报错，这是权衡，别改成翻页到底）。
- `testNodeLatencies` 的 ID 列表超过 100 个要分块，否则 HTTP 超时。
- 老版本 daed 没有 `GroupSubscription` 类型：`qGroupsRich` 会 schema 校验失败，驱动据此
  永久降级到 `qGroups`（`groupsFallback`）。
- **双 schema 兼容（traffic-fix fork 链）——先弄清后端跑的是哪条链**：官方 daed
  v2.1.1 的 git 子模块钉的是**旧 wing `dc50308`**（dae-core `85a1fc3`），这条链的
  上下行流量统计尚未修复——`runtimeOverview` 的 `uploadTotal`/`downloadTotal` 字段
  存在但数据不对，首页速率/图表/累计因此失真。**这是后端钉版问题，不是 dae-tui 的
  bug**；改 dae-tui 治不了它。解决方案：把 daed 仓库的 wing 子模块重钉到
  `b089b56`（"bump dae-core to v2.1.1 and follow its control plane API"，dae-core
  `dbae2e8`，`dae/run.go` 控制面对接重写）并重新构建 daed。本机参考实现：
  `~/projects/daed` 的 `11ff432`（"build(deps): bump wing to b089b56 (dae-core
  v2.1.1, fixes traffic stats)"，嵌套 dae-core 用的是更新的 nightly `b59e375`），
  以 `sudo /home/yang/projects/daed/daed run -c /etc/daed/` 运行——系统里 failed 的
  `daed.service` 是发行版装的旧单元，与实际运行的这个**不是同一个**，别看错。
  新链的 `Global` 类型删了 `soMarkFromDaeSet`（新 wing 的 globalInput 生成器把
  `so_mark_from_dae_set` 当保留字段过滤，见 wing `common.IsReservedConfigField`）、
  新增 `disableThp`/`autoSniffPunt`/`bpfConnStateMapSize`——globalInput 的 SDL 是
  构建期从 dae-core 的 config 结构体反射生成的，核心升级后字段自动出现，两链字段差
  即来源于此。`ListSelections` 先发新字段集（`qSelections`），遇
  `Cannot query field` 永久降级 `qSelectionsLegacy`（冻结 v2.1.1 字段集，
  `selectionsFallback`，同一法则如 groupsFallback）——**两代 daed 都能用**；可编辑
  字段清单仍由 configFlatDesc ∩ 返回键交集决定，老核心自然不出现新字段，无需分支
  硬编码。**误判警告（排查前先对表）**：①「流量显示不对/为零」先确认后端链版本
  ——`selectionsFallback` 是否触发就是现成判据（触发=官方旧链，流量数据天然不准，
  修法是重钉 wing 不是动 dae-tui）；②`qSelectionsLegacy`/`selectionsFallback` 是
  两链兼容的承重墙，不是可以"简化"掉的死代码；③仓库里 `schema.graphql` 冻结的是
  官方 v2.1.1 的 SDL，对照 traffic-fix 链时注意上述 Global 字段差；④本机后端已是
  修复链，别按官方 v2.1.1 的字段集写死假设。
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
- **克隆配置**：`CreateProfile` 的 config 分支用 `createConfig(name)` **返回的 id**
  绑定随后的字段更新（mCreateConfig 本来就选了 `{ id }`）——总共两次请求（create + 一次
  `updateConfigGlobal`）。别改回"创建后 ListSelections 按名字反查 id"：同名未选中配置存在
  时会静默绑错对象，且多一次全量拉取。返回 id 为空时驱动直接报错，不要猜。

## TUI 约定

- **键位提示的位置法则——提示钉在它所隶属容器的底边框上**：只在某个盒内生效的键
  镶该盒底边框（`TitledBoxFooter` / `PaneSpec.Footer`，放不下自动截断、绝不折行）；
  左/右框各自生效的键分居双盒各自的底边（模态打开时右盒 footer 换成该模态的键）；
  **页面级（两种焦点都生效）与全局键才上外框底边**（`helpKeys()` 只保留这些）。不要
  把键位图例写成盒内容行——那是白占一行内容且和边框语言割裂的旧做法（订阅页右栏
  `u/e/x/c` 行、节点页详情底行、各选择器尾行均已因此删除）。写新页面时按 handleKey
  的分发结构归类：focus 分支内的键进对应盒 footer，分支外的进外框。
- **整个应用包在一个圆角外框里，恰好填满终端**：外框由 `ui.AppFrame` 手工构造（lipgloss
  的 Border 嵌不进文字），**帮助键位嵌在外框底边框**（键位先截断、`? 帮助` 固定右端
  不参与截断）。外框内第一层是**页头盒**（`ui.TitledBoxRight`，通栏 cw-2 宽）：页签嵌
  上边框（活动页签 = `TabActive`：加粗+主题色+下划线，非活动页签用 `TabDim` 压暗——
  强调走"线上的文字"这条通道，不用实心底色块，那会打破全应用的线稿语言）、测速指示右对齐在同一条上边框（放不下自动省略）、
  状态行是盒内容；然后是页面本体、toast 裸行（**瞬态消息不进容器**）。`layout()` 的
  chrome 数学 = 外框 2 行 + 页头盒 3 行 + toast 1 行，页面内容高度仍是 `height-6`、
  宽度 `width-2`——改 chrome 行数要同步改这两处与 `View` 里的钳制。终端小于 60×12
  （`minTermW/minTermH`）时渲染 `smallView` 降级页，别让两栏数学溢出换行。登录/
  初始化/连接失败页没有外框，各自用居中的 `TitledBox` 渲染。
- **全页统一边距与标题语言（首页盒网格对齐）**：外框内所有可见行共享同一左基线（外框
  内缩 1 格，右边距 1 格）：页面行与**页头盒行**都在根模型钳制处统一
  `" " + Truncate(line, cw-1)`（首页与各页的盒子宽度已按 cw-2 计算，**不要**在页面里
  再自己加前缀；页头盒跳过这步就会比别的盒子偏左 1 格）；toast 行自带前缀；浮窗用
  `cw-1` 居中。盒子的标题一律"嵌在边框里"（`TitledBox` 嵌上边框，`PaneRow` 会 trim
  标题的历史空格），没有独立标题行。
- **每行都必须过 `ui.Truncate` 到内容宽**：高度数学假设 chrome 各占一行，lipgloss 的
  `Width()` 会把超宽行折行，折一行就挤掉一个 chrome 行。页头盒/外框底边由
  `TitledBoxRight`/`AppFrame` 保证精确宽度（右段放不下即丢弃）；各页双盒由
  `ui.PaneRow` 按宽度截断每行、按高度补齐空行并**钳制溢出**（超出 `H-2` 的行丢弃，
  绝不能把盒子的底边框顶出页面）。盒内容行数正好是各页一直预留的 `height-2`，页面
  窗口计算不用动。
- **选中行高亮一律走 `ui.HiRow`，禁止外层 `Background()` 包裹**：行本身是样式文本，
  每个内嵌样式都以 `ESC[0m` 收尾，外层背景会在第一个内嵌 reset 处被清掉——HiRow 在
  截断补齐后把每个内嵌 reset 后面重新断言一次背景（`reassertBG`）。调用点 = 画 `❯`
  光标的同一处，宽度 = 盒内宽（W-4），背景条必须通长到 w（HiRow 自己补齐）。取色必须
  走 lipgloss（`selBGOpen` 从 Render 剥出 SGR），手写裸转义会绕过 profile 检测、在测试/
  管道里漏出乱码。
- **列表滚动条 `ui.WithScrollbar` 只在溢出时画**（total > 窗口行数）：盒内容右缘 1 列，
  thumb 位置按 start/total 比例。调用方只对"窗口行"应用，pickErr 等附加行必须在它
  **之后** append（否则它们被截掉一列）；不溢出的列表保持原样，窄列表不多列。
- **一次性动作反馈走 `opDoneMsg` toast**（成功 4 秒、失败 8 秒自动消失——失败 toast 走
  `showErrToast`，错误里的路径/行列号 4 秒读不完），不要写进 `pickErr` 这类
  常驻面板字段——它会留到重启才消失，看起来像坏了的状态。`pickErr` 只留给"选择器打开
  期间拉取失败"这种与当前模态绑定的错误，并在 groups/subs 刷新时清空。
- **浮窗与面板的分工——决策和填表用浮窗，浏览和对比用面板**。各页实现 `overlay() *overlaySpec`
  （表单/对话框），根模型 `pageOverlay()` 收集（设置浮窗最优先，`P` 已是全局键，任何页面可开），
  `ui.Overlay` 负责 ANSI 感知地居中叠到页面画面上（`Truncate` 取左 + 补 reset、`TruncateLeft`
  取右——注意 TruncateLeft 会丢弃被跳过区域的转义序列，盒子右侧窄条可能掉色，文字不受影响）。
  浮窗清单：帮助（`helpOpen`，`?` 任何页面）、设置（`settings.open`，`P` 任何页面，含账户/主题等子窗口）、全局 `A` 确认、首页预设确认+DSL 预览、
  各页输入表单（含内置 DSL 编辑器 mode 7）。留在右栏的：小 y/n 确认（删组/删节点/删订阅/
  删配置、组内移除）、大列表选择器（加节点、挂订阅、策略）、DSL diff 确认。
- **内置 DSL 编辑器**（configs mode 7，`config.toml` 的 `editor = "builtin"` 开启）：
  textarea 浮窗，`ctrl+s` → `validateTextCmd(path="")` → `editorValidatedMsg`；校验失败时
  `handleValidated` 检测 `mode==7` 把错误写进 `edErr` 并**保持编辑器打开**；成功进 mode 6，
  `diffState.Builtin` 标记来源，diff 里按 `n`/esc 回到编辑器（内容不丢），按 `y` 直接提交。
  $EDITOR 路径（默认）不变：临时文件在"应用后/内容未变"才删。
- **浮窗期间按键归属**：`helpOpen`、`settings.open`（设置浮窗，含账户/主题子窗口）与
  `logs.open`（日志浮窗）计入 `anyModal()`，且其按键在根模型 `handleKey` 顶部优先分发
  （任何页面可开，esc 不能被所在页吃掉）；
  鼠标滚轮在 helpOpen 时滚帮助、logs.open 时滚日志，其余模态期间忽略。
- 模态渲染在右栏内部的（上面的"留在右栏"清单）用 `ui.BoxLines(destructive, lines...)`
  （红框=破坏性），列表型选择器保持裸行；不能 `body += "\n" + box` 追加在双栏下方——
  面板补齐到满高，下方追加的内容第一个被硬钳制裁掉。
- 首页有跟随滚动：`bodyLines()` 返回行列表+活动行，`View` 让窗口跟随活动行（`follow`
  在任何按键后重新启用；滚轮 `scrollBy` 暂时关闭跟随）。帮助是浮窗，内容用 `helpScroll`
  （j/k/G）在盒内滚动，`clampHelpScroll` 的窗口数来自 `helpWinBody`；帮助内容是
  `helpSections` 表驱动（渲染与 `helpSectionStart` 同源），`?` 打开即定位到当前页的
  小节（测试断言的是新行为，别改回"从顶开"）。
- **主题色只有一处入口**：`config.toml` 的 `accent` / `border` / `dim`（均为 ANSI-256
  序号或 `#rrggbb`）与 `theme`（命名主题：内置 `默认`/`浅色` + 九个色系主题
  （北欧/夜航/布丁/玫瑰/青竹/石青/暖橙/水墨/晨光，accent 刻意避开延迟色阶的
  绿/黄/红）+ `theme/*.toml` 文件，
  名字=文件名主干；主题优先于内联三色，缺省槽位按 主题→内联→`ui.Default*` 级联成
  具体色值——ApplyTheme 把 "" 当"保持现值"，绝不能让它收到空串）经
  `ui.ApplyTheme(accent, border, dim)` 应用；启动时 main.go 解析一次，设置浮窗
  （`settings.go` 的 theme picker）运行时预览/切换并回写 config.toml。它会重建——每个值独立
  解析（`parseColor`，空/非法保持默认，半合法的配置照样主题化能解析的部分）。它会重建
  `TitleStyle`/`TabStyle`/`TabActive`/`HelpStyle`/`SelectedStyle`/`CursorStyle`/`BorderDim`
  这些 init 时从三者派生的包级样式，并重推 `SelBG`
  （`selBGFromAccent`：主题色按 `selAccentMix` 掺进暗灰底，选中条因此跟着 accent 走）。
  `border`/`dim` 是为浅色终端准备的：默认 238/245 按深色终端调，白底下几乎看不见；
  新增样式要么 init 后可被 ApplyTheme 重建，要么在调用时读 `ui.*` 变量，禁止把颜色烤进
  init 字符串。
- **鼠标已启用**（`tea.WithMouseCellMotion`）：滚轮 = 3×j/k（帮助浮窗/首页直接滚偏移），
  点页签切页（页签嵌在页头盒上边框：`y=1`、列从 `x-5` 起——页头盒与页面盒子共享同一
  1 格左边距；`tabClick` 按渲染宽度算
  span），点左栏行选中（外框 1 行 + 页头盒 3 行 + 盒子上边框 1 行，`row = y-5`；各页
  `leftClick` 复算 `leftLines` 的窗口偏移），点外框底边框任意位置打开帮助浮窗（呼出键
  `? 帮助` 固定在该边框右端且**不参与截断**，页签不要放帮助标识）。右栏行点击：群组页
  与订阅页把光标钉到所点行（`rightClick` 同构：扣信息卡 topH 与过滤提示行再映射窗口，
  **且必须带 `d >= rowsH` 上界守卫**——盒底边框行和盒外行（toast 行）是 no-op，
  长列表下少了它会把光标移到窗口外的隐藏行）；配置页右栏点击=聚焦，全局配置区把字段
  光标钉到所点行，**编辑浮窗只由 Enter 打开**（点击直接开表单是误触源，已否决）；
  节点页右栏是静态详情无光标，本来就不需要。
  首页 `click` 映射两个交互区——组行与路由预设行，锚点由 `bodyLines` 随渲染返回
  （`homeAnchors`），click 复用同一份数学不重推；首页扁平行 f 渲染在 `y=4+f-窗口起点`，
  而 row 0 是盒内第一行，所以 **flat = row + 窗口起点 + 1，窗口起点必须走
  `homePage.windowStart`（View 与 click 共用的那个）**——`View` 是值接收者，follow
  调整从不持久化，直接拿 `p.scroll` 会在首页放不下、键盘导航滚过视图后整体错行；
  预设行**只移光标不触发确认**（切换会整替路由 DSL，误点不能直接武装），twoCol 时
  预设行右于页缝、缝左（环境盒）与缝本身的点击不算。`anyModal()` 时鼠标全部忽略——弹窗期间误点比不点更糟。
  **双击 = Enter**（同格 400ms 内第二次按下，`click()` 里检测）：只在 groups/subs/nodes
  三页生效——它们的 Enter 是导航性的（切焦点/开合分区）；configs 左栏的 Enter 直接
  切换生效方案、首页预设的 Enter 会武装 DSL 替换，这两处双击必须保持"只选中"
  （`TestConfigsDoubleClickStaysSelection` 兜底）。合成的 Enter 是 cmd 里的
  `tea.KeyMsg`，测试要执行 cmd 再喂回 Update。
  鼠标 handler 是**值接收者**（与 `handleKey` 一致），别改成指针接收者，否则
  `tea.Model` 的动态类型在键盘/鼠标两条路径上不一致。
- 每个内容页都是**左列表 + 右详情**：左栏 `j/k` 移动，`Tab/l/Enter` 把焦点切到右栏，
  `h/esc` 收回。两栏是 `ui.PaneRow` 拼的一对 `TitledBox`（聚焦盒标题+边框点亮），
  `leftW/rightW` 即盒子**外宽**（左栏上限 38 保内容宽 34，中间 1 格 gutter），加载
  失败/空列表等早退状态也走同一布局（消息进左盒）。**群组页与订阅页的右栏是上下双盒**
  （`ui.PaneRowColumn` + `app.stackedDetail`）：上盒是 content-sized 的信息卡（组=
  策略/成员/引用，订阅=标签/状态/定时/链接/更新），下盒占满剩余高度放节点内容；没有
  "展开后才显示"的门控（旧的 `expanded` 字段已删），订阅页节点**选中即拉取**。信息行
  标签列统一 6 格（`SelectedStyle` 标签 + 两空格），三页一致。配置页左栏是例外（见
  下面"三个等分竖排分区盒"条目）。
  **订阅页的信息盒跟焦点换主题**（`showNodeInfo`）：右栏聚焦时上盒改显选中节点的
  名称/协议/地址/链接/延迟（标题用节点名），Tab 回左栏恢复订阅卡——选中节点因此从
  "视觉状态"变成"我看懂了它、我能操作它"（`T` 单测、右栏 `y` 复制该节点分享链接，
  左栏 `y` 仍是订阅链接；toast 会点名复制的是什么）。盒高必须用 `infoBoxLen()`
  （两种状态行数的较大值）钉死：按当前内容 sizing 会让节点列表在每次 j/k 时重排，
  `View` 与 `rightClick` 用的是同一个值。`selectedNode()` 不跟 focus（光标高亮跟、
  选中不跟），所以 `T` 在左栏也能用；`y` 必须显式判 `focus == 1`。
- **节点列表的过滤/排序统一走 `internal/app/nodelist.go` 的 `nodeView`**（`/` 开过滤框，
  `enter` 保留、`esc` 清空，`o` 循环排序，未测/死亡节点排序时殿后）。群组页右栏、`n`
  选择器、订阅页右栏、手动节点页四处都内嵌它；新增节点列表必须复用，不要另写一套。
  过滤框打开时所有按键归它（并计入 `anyModal()`），`t` 测速只测可见节点。群组页在过滤
  状态下要把分区视为展开、无命中分区直接不建行（见 `rebuild`），并且换组时必须
  `rebuild()`——否则右栏会残留上一个组的行。
- 所有 IO 走 `tea.Cmd`（`msgs.go` 的 `withCtx`/`withCtxT`，默认 12s 超时），
  **`Update` 永不阻塞**。mutation 成功后由同一个 cmd 顺带重新拉列表，返回
  `groupsMsg`/`subsMsg`/`selectionsMsg` 等。
- **mutation 失败路径必须让页面的 `busy` 有着落**：成功走列表消息（handler 清零
  `busy`），失败走 `opDoneMsg`（只有 toast，没有页面收尾）——所以会置 `busy` 的
  mutation cmd（subs/nodes 页）失败返回时必须带 `opDoneMsg.Idle`（`busySubs`/
  `busyNodes`），根模型按它清标志。漏一个字段，页头"处理中"就转到重启（没有周期性
  重拉能救它）。新增 mutation 时照此办理；`groups` 页没有 busy 字段，不受此约束。
- 改节点/订阅用**原地编辑**（`e`，`tagNode`/`updateNode`、`tagSubscription`/
  `updateSubscriptionLink`）：ID 不变，群组挂载才不丢。**浮窗目标在打开时锁定 ID**
  （nodes/subs 的 `opID` + `opNode()/opSub()`）：延迟排序下 3 秒轮询会重排可见列表，
  提交时按 `p.sel` 现算会操作到漂移后的**另一个**节点/订阅——编辑表单的字段更是
  打开时的旧值，写进新目标就是脏数据；目标随刷新消失时 `opGone` 出 toast 而不是
  静默取消（`TestNodesModalTargetLockedAcrossLatencyDrift` 兜底）。删除 + 重新导入看起来等价，
  实际会静默断掉 `groupAddNodes`/`groupAddSubscriptions` 的 ID 绑定——别为省一个表单
  把它改回"删了重导"。
- 批量导入框（手动节点页 `a`）是 bubbles 的 **textarea**（每行一条链接；ctrl+s 或切到
  标签框后 enter 提交，框内 enter 是换行）。导入结果走 `importDoneMsg`：成功数进 toast，
  失败明细渲染在右栏（`importFail`）——一行 toast 说不完逐条报错。
- `P` 是设置浮窗（`settings.go`：sub 0 菜单 / 1 账户菜单 / 2 改密码 / 3 退出确认 /
  4 主题选择器 / 5 关于），已计入 `anyModal()`；esc 在子窗口逐级返回、在菜单关闭。
  账户状态机已从 homePage 迁入根模型；未实现的菜单项 enabled=false（光标跳过、置灰
  加「即将支持」，实现一项开一项）。版本来源：main.go 的 `version` 变量吃 ldflags
  `-X main.version=`，缺省回退 `debug.ReadBuildInfo` 的 vcs 修订号；`app.Version`
  只读展示。盲文徽标已是正式设计：`settings.go` 的 `aboutLogoFrame`（框层：圆角框+
  虚线轴，强调色常规字重）+ `aboutLogoBars`（柱层：六根 2 点柱子，强调色加粗）=
  30×20 点阵上的「框内流量柱」——首页流量火花图的微缩（15 字符宽 × 5 行，柱profile
  5,9,11,10,9,5 单峰近对称，柱底悬在轴上方一行，与 Sparkline 同语法）；`aboutLogoLines()`
  渲染时按格合并两层（含柱点的格整格加粗 accent，其余常规 accent——全图同一主题色，
  层级靠字重与形状密度，别再退回暗灰框：238 在真实终端上几乎看不见），样式渲染期读取
  所以主题预览能换色。
  另五版候选（六边形/盾牌/波浪/节点/宝石）与点阵图在 `logo_test.go` 的目录里，换版=粘贴
  两层 braille（单色候选全进 `aboutLogoBars`、`aboutLogoFrame` 留空行），
  `TestAboutLogoIsKnownCandidate` 比对两层兜底防半 paste；
  退出登录由根模型处理（`logoutMsg`：清 cfg + 存盘 + `drv.Logout` +
  回 `phaseLogin`），页面自己不能改 phase。首页 `L` 是 daed 日志视图：**应用内浮窗**
  （`app/logs.go`，journalctl 作普通子进程流式输出，不再 `tea.ExecProcess` 接管终端），
  j/k 滚动、f 跟随开关、r 重载、q/esc 关闭；跟随中贴底、上滚即脱钩；仅本机有效。
- 全局 `r` 是**全量刷新**：所有列表 + status + traffic + 当前页延迟轮询一次性重拉，
  不是只刷当前页——各页共享组/订阅/方案数据（首页显示组、群组页显示订阅标签、首页
  路由区来自 selections），只刷当前页会让切过去后的视图是旧的。每个请求都用
  `countRefreshCmd` 包了一层 `refreshDoneMsg`（**消息必须是 `tea.BatchMsg{c, done}`，
  不能写 `tea.Batch(...)`——那返回的是 Cmd 不是消息**），最后一个回复清零
  `refreshing` 并弹 `✓ 已刷新` toast（完成无提示会被当成死键），顶栏 `spinSuffix`
  期间显示"刷新中"；回复丢了的兜底是 tickMsg 里的 `refreshStale()`（15s，清零并弹
  超时 toast），没有独立定时器。
- **断线徽章**（`connFails`/`connFailTrip`）：traffic（1s 心跳）与 status（5s 轮询）
  两个探测的失败都计数、任一成功清零，连续 `connFailTrip`=3 次失败即顶栏状态行
  `⚠ 连接断开 · <刷新键> 重试`（红色，**顶替**"● 运行中/○ 未运行"——链接断开时
  运行态是陈词，徽章同时**隐藏右侧冻结速率**，死链上显示速率会被读成当前值）。
  `ErrNeedAuth` 不参与计数（`msgAuthErr` 在进 switch 前就转去 phaseLogin）。
  恢复是自动的（探测成功即清零），`r` 是手动重试且 `forceRefresh` 本身含
  status+traffic 探测——**别给断线另造重试入口**。`!TrafficStats` 的后端只有
  status 探测（15s 才到阈值），这是降级路径不是 bug。
  `TestDisconnectBadgeAfterProbeFailures` 兜底。
- **键位重映射（`internal/keymap` + `app/keybinds.go`）**：动作以"作用域+默认键"标识
  （如 groups/j），keys.toml（config.toml 同目录）按节覆盖默认键；**分发 switch 仍读
  默认键名**——`tk(scope, pressed)` 在各页分发点把按键改写回规范名，改走的默认键返回
  dead 字符串。钩子位置必须避开文本输入捕获分支（subs/nodes 的表单与过滤框、configs
  的 modalKey 都吃原始按键）；nodeView 关闭态的 `/`、`o` 走 `tk`，打开态原文输入。
  `esc/enter/tab/shift+tab/方向键/ctrl+c` 是固定键，加载时拒绝作为新键位；同作用域
  冲突双方都拒绝并记入 notes。**遮蔽检测（load 时第二遍 + Set 时前置）**：新键若是
  同作用域或全局层里其它**未改走**动作的默认键，绑定被拒绝并记 notes——否则 `tk` 会
  把按键改写向新绑定、原动作被静默遮蔽。keymap 包本身不知道默认键表：app 在 loadKeys
  里 `registerKeyDefaults()` 先注册（keyCatalog + nodeView 的 `/`），Load/Set 查询
  （`shadowsDefault`）；交换两个键要在文件里同时写两个绑定（两遍式检测放行），顺序
  Set 需经中间键。外框键位条与页脚经 `K(scope, def)`/`kb()` 拼装，显示的
  永远是当前生效键位。滚轮发 `K(scope,"j"/"k")` 而不是字面 j/k，否则重映射后滚轮失灵。
  appKeys 是包级变量（页面是值类型拿不到 Model），New 里 loadKeys、查看器里 r 重载；
  测试要覆盖它必须在 New 之后赋值。设置内可交互改键（keysKey/applyKeyBinding：Enter
  捕获下一键、同作用域或全局占用/固定键即时拒绝、d 恢复默认），经 keymap.Set 原子写回
  keys.toml（按作用域+键名排序重建，手写注释不保留但所有绑定保留）。
- **i18n（`internal/i18n`）**：中文文案**本身就是 key**——zh 模式 `T()` 原样返回 key（所以断言中文字面量的测试全部照旧），en 模式查 `en` 表、缺失回退 key，en 目录可以滞后于代码。两条铁律：**渲染期文本在 View/render 路径里调 T**（设置里切语言下一帧即生效，语言选择器在 `settings.go` 的 langKey）；**禁止把 T() 结果存进长寿命结构体字段**（包级表如 helpSections/tabLabels/presetLabels 存中文 key、渲染时翻译；toast 等瞬态消息允许构造时翻译，切语言后残留几秒可接受）。`config.toml` 的 `lang = "en"` 切英文，main.go 启动 `i18n.SetLang` 一次——为了让 `-h` 帮助文本也跟着语言走，语言预扫描在
  `flag.Parse` **之前**（`preScanLang` 只从原始 os.Args 抠 `-config` 路径再做一次无副作用
  的 Load，失败静默回退中文）。**表单 placeholder 一律渲染期赋值**（各页 `overlay()`/
  `prompt()` 开头 `p.X.Placeholder = T(...)`，构造器里不存 T()）——textinput/textarea 是
  长寿命字段，构造期翻译会让切语言后 placeholder 停在旧语言。driver 层错误是技术诊断信息，**不翻译**。en 表 key 与 T() 调用点的一致性靠"缺失即回退中文"兜底，别为对齐而维护第二份清单。
  带参数的文案必须让 `T` 直接收格式化参数（`T("%d分钟前", n)`）——先 `fmt.Sprintf`
  再 T 翻译的是已格式化串，目录里永远没有那个 key（TimeAgo 曾因此整体残留中文）。
  数字+词的拼接注意 en 值的空格（`T("%d节点", n)` 出 "24 nodes"，`Itoa+T("节点")` 出
  "24nodes"）。已知包级表（configFieldLabels/sectionTitles/policyChoices/helpSections/tabLabels/
  presetLabels/settingsItems）一律存 key、查表处翻译——脚本曾把 T() 包进 init 表达式
  造成语言冻结（配置页右栏字段标签就是因此残留中文），`TestEnglishModeChromeHasNoCJK`
  是 chrome 无中文的回归兜底。dae 核心的 configFlatDesc 说明文字是英文数据（dae 的
  config/desc.go），f.Desc 原样展示即可，不进目录。
- **`Model.View()` 末尾的硬钳制不能删**：body 行数超过 `height-6`（外框 2 行 + 页头盒
  3 行 + toast 1 行）就截断、不足就补齐空行，否则页签/帮助边框会被挤出屏幕（这是最早
  修的滚动 bug；钳制现在同时负责"撑满终端+页脚贴底"）。
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
- 破坏性操作（删组/删节点/删配置/停止代理/重载）都要 `y` 确认。

## 横切机制（改 UI 前必读）

- **Caps 降级已接线**：`New()` 启动时读一次 `drv.Capabilities()` 存进 `Model.caps` 并传给
  各页面。按键按能力位门控，不支持时 `unsupportedCmd(op)` 出 toast（"当前后端不支持
  该操作"），订阅/配置页还有横幅；`!TrafficStats` 时首页不渲染流量图、轮询也跳过。
  新 driver 必须如实返回 Caps——`stubDriver` 返回全 true，写"残废后端"测试要内嵌它再覆盖。
- **测速进度与忙碌指示**：`testWindow(n) = 15s + 200ms×n`（上限 2 分钟，与
  `testLatencyCmd` 的 ctx 超时一致），三个页面各自 `testProgress()`，根模型 `spinSuffix`
  把盲文 spinner + "测速中 x/y"——进度是**跨页汇总**的（测速中切页指示不消失）。
  **mutation 忙碌（`anyBusy` = subs/nodes 页的 `busy`）同样进 `spinSuffix`，显示
  "处理中"**——别再把进行中的操作画进页面盒内容（旧的"⏳ 操作进行中"行已删）。
  spinner 由 120ms 的 `spinnerMsg` 自续链驱动（`spinning` 防止叠链；1s tick 在测速或
  mutation 开始时拉起链，都空闲时链自灭，空闲 UI 不空转）。
  **鼠标滚轮会重放 j/k 并可能因此产生 cmd——`handleMouse` 的滚轮分支必须把页面
  `handleKey` 返回的 cmd 传出去**：订阅页 j/k 会触发节点拉取（`ensureNodes` 先置
  `loading` 再返回 cmd），丢掉 cmd 就是"拉取节点中"永久卡死。
  完成判定仍是"所有 testIDs 的 `TestedAt` 都新于 baseline"。**超时判定在 err 判定
  之前**（三页 `handleLatencies` 同一法则）：后端持续报错时窗口也必须按时结束，
  否则 `testing` 卡真 + 根模型 tick 每秒重发轮询（`TestLatencyTestWindowEndsOnErrors`
  兜底——曾经的 groups 页 bug）。测速期间的 `testIDs` 轮询
  （每秒）与下面的按页轮询并存，互不影响。
  **完成铃**（`notify.go` 的 `testDoneNotifyCmd`）：根模型 latenciesMsg 分支在派发
  前后各看一次聚合 `anyTesting()`，从有测速变无测速才发一次——响的是"spinner 消失"
  这个事件，批次完成与窗口超时都算，空闲后的后续轮询不响。单次 WriteString 写
  `OSC 9（桌面通知文案）+ BEL`，与 OSC 52 同一单写纪律、同样 `stdoutIsTerminal()`
  门控；BEL 的音量/静音是终端自己的设置，所以**不做配置开关**（用户的关阀在终端侧）。
  `TestLatencyDoneBellRingsOnce` 兜底。
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
- **群组页多选**：`space` 按**节点 ID**标记——**两张 map 各管一种语义**：详情区
  `marked`（`t` 只测已标记、`x` 批量移除已标记的**直接挂载**节点，订阅贡献的只能随订阅
  移除并给 toast 说明）与 `n` 选择器 `pickMarked`（`Enter` 批量添加）。曾经共用一张
  map：详情区勾的要移除节点会被选择器当添加目标发去 `groupAddNodes`，且选择器 esc 会把
  待移除标记一并清掉——**新增"待选集合"一律新建 map，不要复用这两者**。
  `markedIDs()/markedDirectNodes()` 必须去重——同一节点可以同时出现在订阅区和直接区两行。
  换组清空详情标记（`collapseSections`，只在换组时调用——右栏常驻后 esc 离开右栏不再收起
  分区/清标记，否则等于当着用户的面丢数据）。
- **首页分区（btop 式网格盒子）**：整页是带标题的圆角盒子（`ui.TitledBox`，标题嵌在
  上边框、聚焦时点亮标题+边框），按严格网格排布：**全页唯一竖缝**（左列 = `homeRoutingW`，
  行行相同）、**同行盒子等高**（`ui.PaneRow` 把短侧内容补空行到等高再包框，边框上下
  对齐、死区留在盒内——两侧 `PaneSpec.H` 都不钉时它按对侧内容高度配对，**包框后补白行
  不算等高**，那会让短盒底边浮在对侧边框上方；`TestPaneRowContentSized` 与
  `TestHomePairedBoxesShareBorders` 兜底）、盒子行间贴合无空行、盒内内容统一前导 1 格（与徽章文字
  基线一致，`routing` 续行标签对齐 `config` 列）。宽 ≥96 列（`homeTwoColMin`）
  两盒一行：`代理|流量` → `环境|路由`（左列=静态状态，右列=动态与操作），`各组
  当前节点`通栏；窄终端全宽堆叠（顺序 代理→流量→路由→环境→组）。新增分区=建
  rows 函数 + TitledBox 包一层；配对用 `pairedRow`（内部即 `PaneRow`），别手拼。
- **首页重点提亮层级**：一眼要看的（代理状态徽章、上下行**加粗**速率、活动预设 ●、
  组当前节点名）用色块/加粗/亮色；回头查的（方案名、连接/UDP/API、累计、环境事实）
  一律暗色；异常（订阅过期、接口缺失）黄/红。运行配置过期的 "⚠ 需重载 (A)" 提示统一在顶栏状态行（首页代理盒与配置页的本地横幅已删）。徽章行不再带版本号（状态栏
  已有）；`方案 config X · dns Y` + 续行 `routing Z` 在"代理"盒内。
- **首页信息密度（都不新增轮询，数据来自已有消息流）**：
  - **订阅摘要**（`subLines`，根模型在 `subsMsg` 里喂 `home.setSubs`，渲染进"环境"盒）：
    `订阅 N · 节点 M · 最近更新 <TimeAgo>`；cron 开着但 UpdatedAt 超 24h 的订阅逐个
    黄字 ⚠（订阅静默失效是最常见的"代理变慢"根因）。**dae-wing 全库确认：`status` 字段
    只在创建订阅时写死 `""`、刷新流程从不写入（Info 同样从不写入），即 v2.1.1 里恒为
    空**——空状态渲染暗色 "—"，绝不当失败信号；只有 status 含 fail/error 才标红并
    显示 info 首行（订阅页左栏），新鲜度才是诚实指标。网卡行同盒（`netLines`，前缀"网卡"）。
  - **组行健康后缀**（`groupHealth`，盒内容宽 ≥68 才显示）：`存活/已测 · <TimeAgo>`，
    **只用 `p.lat` 里已有的数据**（别的页轮询到的），必须带年龄标注，未测组显示
    "未测速"而不是 0/0；**右对齐到盒内右缘**。
  - **流量脚注**：`trafficMsg.Took`（trafficCmd 计时）显示 `API Nms`（SSH 隧道健康
    线索），累计行标注"自 daed 启动"（重启清零，不是月流量）。**峰值贴在速率行里**
    （每个方向"当前 · 峰值"，图表就在其下方）：速率行按盒子实际宽度排版、不强制和
    图表共用列宽（否则多出来的峰值正好被截掉）；放不下时峰值退回脚注，脚注自身放不下
    时拆回两行——合并行被盒子截断会静默丢掉 UDP 计数。
  - **规则速览盒是页底填充物**（`rulesDigest` + `bodyLines` 尾部的 digest 分支）：
    通栏堆在"各组当前节点"之后，内容 = 引用组缺失 ⚠ 行（`routingRefs` ∩ 现有组名，
    内置 outbound 跳过）+ 选中路由的 `Summary`（驱动已归一化、must_direct 已拼回），
    **只装路由，DNS 不进这个盒**——DNS 与规则混排时 DNS 行读起来像条规则、规则读起来
    像噪音（用户明确否决过），DNS 方案与上游归环境盒的 `dnsLines`。数据全部来自
    `ListSelections` 已拉回的内容（`handleSelections` 保留
    `routingSummary/routingRefs/dnsSummary`）。**只在剩余高度 ≥
    `rulesDigestMinInner` 时渲染、内容超出就窗口化并给"… 其余 N 条"尾行、不足就
    补空行撑到页底**——它是"底部空旷"的解决方案，空间不足时整盒让位、由组盒补空行
    撑底（两条路径保证 bodyLines 行数恰好 `p.height`，`bodyLines` 长度 == 页高有
    测试兜底）。看全量规则是配置页的职责，别把摘要做成第二份全文。
  - **环境盒的 DNS 块**（`dnsLines`，订阅摘要与网卡行之间）：`DNS  <方案名>` 一行 +
    每条上游单独一行悬挂（上游内联会被 40 列的双栏环境盒截掉——和网关行同一个教训）。
    网卡行（`netLines`）默认路由行的网关同样自占一行悬挂（前缀多两格、嵌在网卡名下），
    不参与行内截断。
  - **组盒汇总行**（`latSummaryRow`，组行之后暗色一行）：`全部 N 节点 · 已测 M ·
    存活 K · 最快 <名> · <TimeAgo>`——跨组成员按 ID 去重，**无毫秒**（与组行健康
    后缀同一条诚实法则），已测 0 时显示"未测速"。它占一个内容行：组盒底边框从
    `groupStart+len(groups)` 移到 `+len(groupRows)`，点击映射与边框 no-op 测试跟着
    这个数走。
- **首页组行跳转**：`Tab` 在路由选择器与组列表间切焦点（`home.groupFocus`），组行
  `Enter` 发 `gotoGroupMsg{ID}`，根模型开群组页、选中该组并聚焦右栏（`focus=1`，
  并 `collapseSections()` 清上一组的标记/分区——与 `selectGroupAt` 同一套重置）。
  焦点在组列表时只吞导航键，`o/P/L/g` 等仍走原路径。
  **全页恰好一个光标**：`routingLines` 的预设行光标/高亮以 `!p.groupFocus` 门控
  （组行光标以 `p.groupFocus` 门控，两侧对称）——聚焦组列表时预设行只留 ● 状态标记，
  同时亮两个 ❯ 会被读成"选中没切走"（`TestHomeSingleCursorAcrossFocus` 兜底）。
- **订阅页节点缓存**：`subsMsg` **不再**清空 `subNodes`；只有 `u`（更新）把对应 ID 记入
  `stale`，下次 `handleSubs` 时删那一条（并顺带清理已删除订阅的残留），根模型随后
  `ensureNodes` 重取。节点列表常驻右栏下盒，`ensureNodes` 只看缓存与 loading——j/k
  移动、左栏点击、`subsMsg` 后都会触发拉取，任何地方调用都安全。
- **群组页分区展开按订阅 ID 键定**（`subOpen map[string]bool`）：刷新后订阅重排不会
  把展开状态错位到别的订阅。`directOpen` 是整个直接区的单个 bool。
- **DSL 编辑流程**：`$EDITOR` 退出 → 后端校验 → **diff 确认**（`configsPage.diff`，
  mode 6，`y` 提交/`n` 取消）→ 才 `configTextCmd`。临时文件只在"应用后"和"内容未变"
  删除；校验失败和用户取消都保留并在 toast/右栏说明路径。diff 用 `diff.go` 的 LCS，
  长相同行折叠成 gap 标记。
- **配置页左栏是三个等分竖排的分区盒**（全局配置/DNS/路由规则，btop 式分区）：
  没有分区标题行，`rows` 是纯条目列表（`rowRef{section, index}`，无 kind）。
  `Tab`/`shift+Tab` 在三盒间循环切换（`sec` 活动盒、`secCur` 每盒记住光标、
  `secRange` 界定盒内行界），`j/k`/`g/G` 只在当前盒内移动、活动盒点亮并在其底边框
  携带左栏键位（`leftFooter`）；进右栏用 `l`/`Enter`/`→`，本页 `Tab` **不**切栏
  （外框键位与 help 浮窗都写明）。所有键一律作用于光标所指条目（`item()` 没有
  header 特例，`c` 克隆的就是光标处方案）；光标恒在活动盒内（`setCursor` 同步
  `secCur`，刷新后按它落位）。渲染走 `leftBoxes`（`leftShares` 等分高度、
  `leftWindow` 窗口跟随光标），`View` 用 `ui.JoinBoxes` 手工拼左右两列（不走
  `PaneRow`——它会把整块重新包框）；左栏点击映射 `leftClick` 必须与 `leftWindow`
  的窗口数学一致且会激活所点盒子。页面内容高度 <9 行时 `flatLeftBox` 退化为单个
  扁平盒子（tiny terminal 兜底，否则每盒连一行内容都放不下）。右栏全局配置区是
  **字段表 + 字段光标**（`fieldCur`），且它是**唯一的字段编辑面**——旧的字段选择器
  （原 mode 1）已删，右栏不存在第二份字段列表：j/k/g/G 移动光标（渲染窗口跟随，
  `fieldWindowStart` 与 `rightClick` 共用同一份行映射 `fieldLineOf`——告警行占一行，
  两边都要计），点击只钉光标不进表单（编辑浮窗由 Enter 开），点击同时回收焦点到左栏（focus=0，与 nodes 的 leftClick 一致——聚焦右栏时点左栏是"我要操作左栏"的明确信号），`e` 在全局配置区=
  聚焦右栏字段表。字段编辑浮窗（mode 2）的 esc 与提交都一步回字段表、光标停在
  原字段——不要让它"返回"某个中间列表；`rebuild`（刷新）对 fieldCur 只钳制不归零，
  多字段连续编辑的节奏不被刷新打断，归零只属于显式导航（`setCursor`/
  `switchSection`）。DNS/路由右栏仍是纯滚动视图（`p.scroll`），无光标。
- **字段输入预校验**（`fields.go: validateFieldValue`）：按 `ConfigField.Type`
  （int/bool/duration/array）在客户端挡掉明显非法的值，错误显示在输入框模态内，
  不提交。语义仍然归后端。
- **`tea.ExecProcess` 出来必须补 `reenableMouse()`**（`app.go`）：bubbletea v1.3 的
  ReleaseTerminal 会关掉鼠标上报，RestoreTerminal 只恢复 altscreen/括号粘贴/焦点上报、
  **不恢复鼠标**——不加这条，$EDITOR 退出后点击/滚轮全部静默失效。目前挂在
  `editorDoneMsg` 分支（日志视图已改为应用内浮窗、不再 ExecProcess），新增 ExecProcess
  调用点要同样处理。
- **日志视图（`app/logs.go`）**：首页 `L` 打开**应用内浮窗**——journalctl 是普通子进程
  （`journalctl -u daed -n 300 --no-pager -f`，stdout/stderr 合并进同一条管道），读行
  goroutine 经 channel 流式喂给 `logsWaitCmd` 链（每行一个 msg，handler 重挂下一条）。
  键位：j/k/g/G 滚动、`f` 跟随开关（暂停=SIGTERM 子进程，恢复=`-n 0` 续流不重放历史）、
  `r` 重载（杀掉重来、缓冲清空）、q/esc 关闭；跟随中贴底自动滚，上滚即脱钩。缓冲上限
  `logsMaxBuffer` 环形丢头。**消息必须带链路标识**（`logsLineMsg.lines`/`logsStoppedMsg.lines`
  与 `m.logs.proc.lines` 比对）：换链后旧链的回声直接吞掉，不能清掉新链的 following。
  本地 exec，远程隧道场景天然不可用（LookPath 失败时 toast 说明）。测试 seam：
  `startLogsProc` 是包级 var，`logs_test.go` 的 `withFakeLogs` 换成手喂 channel 的假进程，
  绝不在测试里跑真 journalctl。
- **CLI 子命令**（`main.go`）：`-cmd status|test|groups|switch-dns|switch-routing`，
  `test` 支持 `-g 组名` / `-n id,id`，switch 族用 `-name 方案名`（`resolveSelection`
  按名反查 ID，miss 时列出可用名）。`groups` 只打印组行（status 的脚本友好切片，
  格式与 `printGroups` 共用）。**switch 只选择不重载**：方案切换在 daed 侧只是标记
  运行配置过期，重载是确认门控的破坏性操作（TUI 内 `A`），CLI 静默重载代理是
  意外突变——打印"重载后生效"提示而不是替用户按下去。刻意没有组切换类子命令：
  fixed 组改组是破坏性操作，理由见"已知限制"。

## 测试约定

- **CI**（`.github/workflows/ci.yml`）：push/PR 跑 `go build ./...` + `go vet ./...` +
  `go test ./...` + `go test -race ./...`（Go 版本取 go.mod）。race 档守的是
  config/i18n 共享状态契约：后台 token 刷新在锁内 marshal 整个 config，UI 侧任何
  字段写都必须走加锁 setter（`UpdateToken`/`UpdateCredentials`/`UpdateTheme`/
  `UpdateLang`），别再写 `m.cfg.X = ...`。改任何东西前先本地过这三样；bubbletea v1.3
  `ReleaseTerminal` 关鼠标上报那类回归就是靠 CI 里的无头渲染冒烟兜底的。
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
  执行 `tea.ExecProcess` 的 cmd（$EDITOR）**不能**在测试里跑；日志浮窗走
  `startLogsProc` seam、假进程随便跑。
  一个 cmd 可能是 `tea.Batch`（如 editorDoneMsg 顺带 reenableMouse）：用 `execCmds` 展开、
  `firstMsgOf[T]` 取目标消息，别对 `cmd()` 直接做单类型断言。

## 安全与兼容

- `internal/config` 的 config.toml 含密码和长期 JWT，写死 0600；**不要把凭据/token
  打进日志或错误信息**。`Config` 内嵌互斥锁（**只能按指针用**）：持久化一律走
  `UpdateToken`/`UpdateCredentials`/`ClearSession`/`Save`，写入是同目录临时文件 +
  fsync + rename 的**原子替换**（写一半崩溃不会截断唯一凭据副本，且每次都强制
  0600）；后台 hook（token 刷新）与 UI logout 并发写靠锁串行化。改密后驱动先落
  密码再落 token——崩溃窗口留下"旧 token+新密码"，静默 re-auth 能自愈，反过来
  不行。
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
