# dae-tui

`dae-tui` 是 [dae](https://github.com/daeuniverse/dae) 的终端管理界面（TUI）。它通过 [daed](https://github.com/daeuniverse/daed) 的 GraphQL API 管理代理，不直接链接 dae/dae-wing。

## 功能

- 查看代理状态、实时流量和节点延迟
- 浏览路由组，切换策略、节点和固定节点
- 管理订阅与手动节点（支持批量导入、原地编辑、测速）
- 切换并编辑 config、DNS、routing 方案
- 首页提供 GFW、中国列表、全局代理等路由预设
- 支持中文/英文、主题、鼠标操作和键位重映射
- 提供 `-probe` 与 `-cmd` 非交互命令，便于脚本调用

## 快速开始

> 完整的上手走查（含测速、固定节点、路由预设、配置编辑、FAQ）见 [docs/quickstart.md](docs/quickstart.md)。

### 1. 准备 daed

先安装并运行 daed，并确认 GraphQL API 可访问。默认 API 地址为：

```text
http://127.0.0.1:2023/graphql
```

首次运行 `dae-tui` 时，如果 daed 尚未创建账户，程序会进入初始化表单；否则会显示登录表单。

### 2. 下载或构建

从 GitHub Releases 下载对应平台的二进制，或使用 Go 1.27.1+ 从源码构建：

```bash
git clone https://github.com/yhyld/dae-tui.git
cd dae-tui
go build -o dae-tui ./cmd/dae-tui
```

### 3. 启动

```bash
./dae-tui
```

指定 API 地址或配置文件：

```bash
./dae-tui -endpoint http://127.0.0.1:2024/graphql
./dae-tui -config ~/.config/dae-tui/config.toml
```

登录成功后，用户名、密码和 JWT 会保存到 `~/.config/dae-tui/config.toml`，文件权限为 `0600`。JWT 过期后，程序会使用保存的凭据自动续期。

## 常用命令

```bash
# 只读检查后端连接
./dae-tui -probe

# 打印一行式状态
./dae-tui -cmd status

# 列出路由组
./dae-tui -cmd groups

# 测试全部节点
./dae-tui -cmd test

# 测试指定组或节点
./dae-tui -cmd test -g proxy
./dae-tui -cmd test -n node-id-1,node-id-2

# 切换 DNS / routing 方案（只选择，不重载）
./dae-tui -cmd switch-dns -name default
./dae-tui -cmd switch-routing -name default
```

方案切换、订阅更新和群组修改通常需要在 TUI 中按 `A` 确认重载后才会生效。

## 常用键位

| 键位 | 作用 |
| --- | --- |
| `1`–`5` | 首页、群组、订阅、手动节点、配置 |
| `j` / `k` | 上下移动 |
| `Tab` / `l` / `h` | 切换或返回左右栏 |
| `t` / `T` | 测速当前列表、组或节点 |
| `r` | 全量刷新 |
| `A` | 确认重载代理配置 |
| `P` | 打开设置 |
| `?` | 打开帮助 |
| `q` | 退出 |

每个页面的完整键位可按 `?` 查看。设置中的“快捷键”可以修改字母和数字键位，配置文件位于 `~/.config/dae-tui/keys.toml`。

## 配置

配置文件默认位于：

```text
~/.config/dae-tui/config.toml
```

常用选项：

```toml
# lang = "en"                 # 默认中文，可改为英文
# endpoint = "http://127.0.0.1:2023/graphql"
# editor = "builtin"           # DNS/routing 使用内置编辑器
# theme = "浅色"
# auto_reload = true
```

也可以在设置窗口 `P` 中切换语言、主题和自动重载。主题支持内置主题，以及 `~/.config/dae-tui/theme/*.toml` 下的自定义主题。

## 开发

项目只依赖 Bubble Tea、Bubbles、Lip Gloss、BurntSushi/toml 和 Charmbracelet ANSI 工具包，不需要 code generation。

```bash
go test ./...
go vet ./...
go build -o dae-tui ./cmd/dae-tui
```

建议使用本地 API-only daed 做无副作用联调：

```bash
daed run --api-only -l 127.0.0.1:2024 -c /tmp/daed-dev
./dae-tui -endpoint http://127.0.0.1:2024/graphql
```

API-only 实例没有 dae 核心，节点测速接口可能返回 `record not found`，这是预期行为。

## 故障排查

- **连接失败**：确认 daed 正在运行，并检查 `-endpoint` 是否指向 `/graphql`。
- **需要重复登录**：删除或备份 `~/.config/dae-tui/config.toml` 后重新登录。
- **流量统计异常**：检查 daed 使用的 wing/dae-core 链版本；旧链的统计数据可能不准确。
- **配置修改未生效**：方案切换后按 `A` 执行重载。
- **终端显示错位**：使用支持 UTF-8 和 ANSI 颜色的终端，并确保窗口至少为 60×12。

## 许可证

本项目采用 MIT 许可证，详见 [LICENSE](LICENSE)。
