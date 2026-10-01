# 裸机安装 daed（wing 重钉版，修复流量统计）

> 适用场景：不用 Docker，在 Linux 裸机上自建 daed，并把官方钉死的旧 wing 子模块
> 换到修复链，解决「上下行流量统计不到/数据不对」的问题。
> 本教程的每条命令都在本机参考实现上跑通过（见文末「参考实现」）。

## 背景：为什么流量统计不对

daed 的 Go 后端是 `wing` 子模块，wing 里又嵌着 `dae-core`（eBPF 核心）子模块。
官方 daed v2.1.1（2026-09-24 已归档，schema 冻结）钉的是**旧链**：

| 组件 | 官方 v2.1.1 钉的 | 重钉后（本教程） | 本机参考实现 |
|------|------------------|------------------|--------------|
| daed | v2.1.1 (b3043aa) | v2.1.1 + 一次本地提交 | v2.1.1 + `11ff432` |
| wing | `dc50308` | `b089b56`（wing main HEAD） | `b089b56` |
| dae-core | `85a1fc3` | `dbae2e8`（v2.1.1） | `b59e375`（nightly） |

旧链的 `runtimeOverview` 里 `uploadTotal`/`downloadTotal` 字段存在但**数据不对**——
首页速率/图表/累计全部失真。这是后端钉版问题，改 dae-tui 治不了它。
修复在 dae-core v2.1.1 的控制面 API 重写里，wing 的 `b089b56`
（"bump dae-core to v2.1.1 and follow its control plane API"，2026-09-23）跟进对接。

dae-tui 侧的现成判据：设置 → 关于 → **链路自检**。官方旧链会黄字提示
`selectionsFallback`（wing 未重钉、流量统计失真）；修复链显示绿字 ✓。

## 0. 依赖

- Linux x86_64/arm64，**root 权限**（dae 要挂 eBPF）。内核需满足 dae 的 eBPF
  要求（官方要求 ≥ 5.8，建议 5.15+/6.x）。
- `git`、`make`、`gzip`
- **Go ≥ 1.26**（wing 与 dae-core 的 go.mod 都写 1.26；本机用 go1.27.1 构建通过）
- **Node + pnpm ≥ 10.24**（只用于构建前端；本机 pnpm 11.26 + Node 26 实测通过）
- **不需要 clang/llvm**：dae-core 的 eBPF 目标文件（`control/bpf_bpfeb.o`、
  `bpf_bpfel.o`）是预编译提交在仓库里的

## 1. 克隆与重钉

```bash
# 官方仓库已归档、main 不再前进，浅克隆到 v2.1.1 tag 即可
git clone --branch v2.1.1 --depth 1 https://github.com/daeuniverse/daed
cd daed

# 先让 make 建好子模块 stamp（之后 make 不会再擅自重置子模块）
make submodule

# 重钉 wing 到修复链（b089b56 = wing main HEAD）
git -C wing fetch origin main
git -C wing checkout b089b56
git -C wing submodule update --init --recursive   # 带出 wing 新记录的 dae-core v2.1.1 (dbae2e8)

# 把重钉记成一次本地提交
git add wing
git commit -m "build(deps): bump wing to b089b56 (dae-core v2.1.1, fixes traffic stats)"
```

**（可选）dae-core 换更新的 nightly**——wing@b089b56 自带的 dae-core v2.1.1
（`dbae2e8`）已足以修复流量统计；nightly `b59e375` 只是再新几天的核心。本机参考
实现走的就是这一档：

```bash
git -C wing/dae-core fetch origin
git -C wing/dae-core checkout b59e375
```

> ⚠️ 顺序陷阱：nightly 的 checkout 是 wing 子模块里的**工作区状态**，不会被
> daed 的任何提交记录。`make` 的 submodule 目标会把子模块重置回「上层 index
> 记录的版本」——所以必须先 `make submodule` 建好 stamp 再 checkout nightly，
> 否则第一次 `make daed` 会把它悄悄拉回 `dbae2e8`。拿不准就
> `SKIP_SUBMODULES=1 make daed`。

## 2. 构建

```bash
make daed
# 产物：./daed（约 60MB，web 前端已嵌入）
```

`make daed` 自动完成三件事：wing `deps`（生成 GraphQL resolver）→
`pnpm i && pnpm build`（前端产物进 `dist/`）→ wing `bundle`
（把 dist 压缩嵌入 `webrender/web`，`go build -tags=embedallowed`）。
可自定义版本号：`make daed VERSION=v2.1.1-wingfix`。

装到位：

```bash
sudo install -m755 daed /usr/local/bin/daed
```

## 3. 运行

```bash
sudo /usr/local/bin/daed run            # -c 默认就是 /etc/daed，目录自动创建（0750）
```

常用参数（`daed run --help` 可查全量）：

| 参数 | 默认 | 说明 |
|------|------|------|
| `-c` | `/etc/daed` | 数据目录（sqlite db + 配置），升级二进制不用动 |
| `-l` | `0.0.0.0:2023` | GraphQL 监听地址，**纯 HTTP** |
| `--api-only` | off | 只起后端不起 dae 核心（联调用；无 eBPF，**测不了速**） |
| `--logfile` | stdout | 日志落盘 |

首次运行打开 `http://127.0.0.1:2023` 完成 Web UI 管理员初始化。

安全注意：默认监听 `0.0.0.0` 且是纯 HTTP。远程机器建议
`-l 127.0.0.1:2023` + SSH 隧道（dae-tui 的既定用法），或用防火墙挡住 2023。

### （可选）systemd 单元

```ini
# /etc/systemd/system/daed.service
[Unit]
Description=daed (wing b089b56, traffic-stats fix)
After=network-online.target
Wants=network-online.target

[Service]
# dae 核心要挂 eBPF，进程保持 root
ExecStart=/usr/local/bin/daed run -c /etc/daed
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now daed
```

如果之前用发行版包装过 daed，先卸掉，别让新旧两个 daed 混淆；包装残留的
not-found/failed 状态用 `systemctl reset-failed daed` 清理（本机那条 failed 的
`daed.service` 就是发行版残留，与自建的这个无关）。

## 4. 验证流量修复

1. **dae-tui 链路自检**：登录后 `P` → 关于 → 链路自检行显示修复链绿字 ✓
   （`selectionsFallback` 未触发 = 新链；黄字 = 还在官方旧链上）。
2. **首页速率/图表/累计**：造一点流量（开关代理前后对比），速率与累计应真实变化。
3. daed Web UI 的 dashboard 流量图表同样应动起来。

## 5. 已知差异与坑

- **Global 字段两链不同**：新链删掉 `so_mark_from_dae_set`（新 wing 把它当保留
  字段过滤），新增 `disableThp` / `autoSniffPunt` / `bpfConnStateMapSize`。
  globalInput 的 SDL 是构建期从 dae-core config 结构反射生成的——字段清单与
  官方 v2.1.1 文档不一致属正常，旧数据目录直接沿用没问题。
- **`run --dry`（GraphQL `run(dry: true)`）是停止代理，不是校验**，别当 dry-run 用。
- 测速只能在真实运行（带 eBPF）的实例上验证；`--api-only` 实例测速返回
  record not found。
- 上游已归档：schema 冻结不会再有破坏性变更，**也不会有安全修复**；这个重钉版
  属于本地维护版本，升级 = 重跑一遍本教程换更新的 dae-core。
- 重钉后流量仍不对：先确认跑的确实是新二进制（dae-tui About 自检
  `selectionsFallback` 是否触发），别去改 dae-tui——那是后端链版本问题。

## 参考实现（本机）

- 仓库：`~/projects/daed` @ `11ff432`（v2.1.1 + wing 重钉提交，dae-core 工作区
  停在 nightly `b59e375`）
- 二进制：`~/projects/daed/daed`（go1.27.1 构建，`go version -m` 显示模块
  `github.com/daeuniverse/dae-wing v0.0.0-20260923111342-b089b568649f`）
- 运行：`sudo /home/yang/projects/daed/daed run -c /etc/daed/`
