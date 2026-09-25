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
```

Go 1.27.1（go.mod 写 1.27.1）。直接依赖仅 5 个：bubbletea / bubbles / lipgloss /
BurntSushi-toml / charmbracelet/x/ansi。**不要引入新依赖**，除非确有必要。

联调（无副作用）：`daed run --api-only -l 127.0.0.1:2024 -c /tmp/daed-dev` 起一个无
eBPF 的纯 API 实例，再用 `-endpoint http://127.0.0.1:2024/graphql` 指向它。注意
`--api-only` 实例没有 dae 核心，`testNodeLatencies`/`nodeLatencies` 会返回
"record not found"——测速只能在真实运行的 daed 上验证。

## 目录与分层

```
cmd/dae-tui/main.go   入口：flag 解析、config 加载、driver 装配、probe 模式
internal/driver/      Driver 接口 + 领域类型 + Caps 能力位（**不含任何 daed 细节**）
internal/driver/daed/ daed GraphQL 驱动（client/auth/queries/types/driver + schema.graphql）
internal/app/         Bubble Tea 应用：根 model + 各页面（home/groups/subs/nodes/configs/login/help）
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

## TUI 约定

- 每个内容页都是**左列表 + 右详情**双栏：左栏 `j/k` 移动（默认折叠），`Tab/l/Enter`
  展开到右栏（右栏有自己的高度与滚动），`h/esc` 收起。右栏内容用 `ui.Pane` 渲染。
- 所有 IO 走 `tea.Cmd`（`msgs.go` 的 `withCtx`/`withCtxT`，默认 12s 超时），
  **`Update` 永不阻塞**。mutation 成功后由同一个 cmd 顺带重新拉列表，返回
  `groupsMsg`/`subsMsg`/`selectionsMsg` 等。
- **`Model.View()` 末尾的硬钳制不能删**：body 行数超过 `height-4` 就截断，否则页签/帮助
  条会被挤出屏幕（这是最早修的滚动 bug）。
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
- 延迟色阶：未测/死亡 = 灰，<200ms 绿，<500ms 黄，其余红；自动策略下的"当前节点"是
  估算值，显示时加 `≈` 前缀。
- 破坏性操作（删组/删节点/删配置/停止代理/应用配置）都要 `y` 确认。

## 测试约定

- driver 层用 `httptest.Server` mock GraphQL，覆盖 auth 流程、access-denied 自动重试、
  分页拉全、mutation 请求体构造、String 型 totals 解析。
- app 层用 `stubDriver`（内嵌 `driver.Driver` 接口以便只覆盖需要的方法）喂罐头数据，
  做**无头渲染冒烟**：驱动 `Update` + `View`，断言关键文案出现/不出现，并断言按键
  是否产生了 `tea.Cmd`。改 UI 先跑 `go test ./internal/app`。
- 断言用的是中文字面量（如 `"确认删除群组"`），改文案会破坏测试，两边一起改。

## 安全与兼容

- `internal/config` 的 config.toml 含密码和长期 JWT，写死 0600；**不要把凭据/token
  打进日志或错误信息**。
- daed 默认监听 `0.0.0.0:2023` 纯 HTTP；本工具只走回环 + SSH 隧道，不要改这个前提。
- daed 已归档、schema 冻结：这是特性不是 bug——永远不会有破坏性变更，但也不会有安全
  修复。`schema.graphql` 是固化的 SDL 副本，用于回归对照，不参与编译。

## 其他

- **仓库目前没有任何 commit**（全部文件未跟踪）。除用户明确要求，不要 `git commit`。
- `.zcode/plans/` 下是最初的实施计划，可当历史背景读，但目录结构已与实际代码有偏差
  （实际没有 `app/pages/`、`app/keys.go`、`auth.go`），以代码为准。
- 未来方向（接口已预留，未实现）：裸 dae 驱动（Phase 2）、clash-api 驱动。动
  `driver.Driver` 接口时保持向后兼容的加法式演进。
