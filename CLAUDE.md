# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 本地开发规范

- 要求每次修改完后，需要帮我格式化 Go 源码文件（`gofmt -w`）。
- 测试过程中产生的脚本和 build 中间文件，如果没有必要，则删除。
- 所有的 feature 变更，都需要使用分支进行开发。
- 在我未同意之前，你不能推送到远程。
- 我需要：1. 本地 review；2. 远程 PR review。
- 不要过度设计，保持代码的简洁和易读。
- 使用中文注释，一定要简洁明了。专业名词可以用英文。

## PR Review 重点

- 重点：PR 代码中如果出现大量的 JS 注入（`MustEval` / `Eval`）行为，要检查一下是否是必须的，如果可以用 go-rod 的元素操作替代的话，则直接评论需要用 go-rod 行为替代。

## 常用命令

```bash
# 首次登录（必须有界面，会把 cookies 写到 cookies.json）
go run cmd/login/main.go

# 启动服务（默认无头模式，监听 :18060）
go run .
go run . -headless=false          # 带浏览器界面，调试 DOM 时用
go run . -port :3001              # 换端口
go run . -bin /path/to/chrome     # 指定浏览器二进制（等价于 ROD_BROWSER_BIN）
XHS_PROXY=http://user:pass@host:port go run .

# 测试
go test ./...
go test ./xiaohongshu -run TestSearch -v   # 单个测试

# 构建（CI 里对 5 个平台各构建 mcp + login 两个二进制）
go build -o xiaohongshu-mcp . && go build -o xiaohongshu-login ./cmd/login

gofmt -l . && go vet ./...
```

注意：本机是 CRLF 检出，`gofmt -l .` 会把几乎所有文件都列出来（只是行尾差异）。只对自己改过的文件跑 `gofmt -w`，不要全库重排。

`xiaohongshu/` 下的测试大多是需要真实登录态和浏览器的端到端脚本，默认都带 `t.Skip("SKIP: 测试发布")`，只有想手动跑某条链路时才临时去掉 skip——不要在提交里把 skip 删掉。真正在 CI 跑的纯单元测试只有 `pkg/downloader` 和 `pkg/xhsutil`。

验证 MCP：`npx @modelcontextprotocol/inspector`，连 `http://localhost:18060/mcp`。

## 架构

一句话：一个 Go 进程同时暴露 **MCP (Streamable HTTP)** 和 **REST API**，两者共用同一个 service 层，底层全部通过 **go-rod 驱动真实 Chrome** 操作小红书网页——没有调用任何官方 API。

分层（从外到内，全部在 `package main` 的根目录文件里，除了最内层）：

1. `main.go` → 解析 flag，写入 `configs` 全局（headless / binPath），构造 `XiaohongshuService` 和 `AppServer`。
2. `app_server.go` + `routes.go` → 一个 gin engine 同时挂载：
   - `/mcp` 与 `/mcp/*path`：官方 `modelcontextprotocol/go-sdk` 的 `StreamableHTTPHandler`（`JSONResponse: true`）。
   - `/api/v1/*`：REST 版本的同一批能力（见 `docs/API.md`）。
3. 两套 handler，**同一个 service**：
   - `mcp_server.go`：工具的 schema 定义（`XxxArgs` 结构体 + `jsonschema` tag 就是 LLM 看到的参数说明）与 `mcp.AddTool` 注册，每个 handler 都包一层 `withPanicRecovery` 把 panic 转成 `IsError` 结果而不是打挂进程。
   - `mcp_handlers.go`：把 MCP 参数转成 `map[string]interface{}` 调 service，返回面向 LLM 的文本 `MCPToolResult`。
   - `handlers_api.go`：gin handler，返回 `SuccessResponse` / `ErrorResponse` JSON。
4. `service.go`（`XiaohongshuService`）：业务编排与校验（标题 ≤20 字、定时发布 1 小时~14 天、图片下载/本地路径处理）。**每个方法自己 `newBrowser()` + `NewPage()` 并 defer 关闭**——一次调用一个浏览器实例，无共享状态、无连接池。
5. `xiaohongshu/`：唯一接触页面的地方。每个文件一个 "Action"，构造函数即完成导航（例如 `NewFeedsListAction` 内部就 `MustNavigate` 了），方法执行具体交互。

支撑包：`browser/`（组装 headless_browser 选项、注入 cookies、读 `XHS_PROXY`）、`cookies/`（cookies 文件读写与路径解析）、`configs/`（headless/binPath/图片临时目录全局）、`pkg/downloader`（URL 图片下载 + 格式校验）、`pkg/xhsutil`（标题长度按中文字/英文单词计算）、`errors/`（`ErrNoFeeds` 等哨兵错误）。

### 需要知道的几个隐含约定

- **站点域名是 `rednote.com`**（`www.rednote.com` / `creator.rednote.com`），不是 `xiaohongshu.com`。改导航 URL 前先看 `xiaohongshu/navigate.go`、`publish.go`、`search.go`。
- **两种数据提取路径**：首页 feeds 走 `window.__INITIAL_STATE__`（`feeds.go`）；搜索结果页 rednote.com 不注水 `__INITIAL_STATE__`，只能走 DOM 提取 fallback（`search.go: extractFeedsFromDOM`）。加新页面时先确认是哪种。
- **`xsec_token` 是贯穿全流程的必需参数**：详情、评论、点赞、收藏、用户主页都要，且只能从 feed 列表/搜索结果里带出来，不能凭空构造。
- **搜索筛选器靠 `FiltersIndex`/`TagsIndex` 这种位置索引点击**（`search.go` 顶部的表），页面上有隐藏的重复节点，所以索引不连续（例如"图文"是 5 而不是 3）。筛选器失效时优先怀疑这张表。
- **cookies 路径有向后兼容逻辑**：`cookies.GetCookiesFilePath()` 先看 `$TMPDIR/cookies.json`（老路径，存在就继续用），否则 `COOKIES_PATH` 环境变量，最后 fallback 到工作目录的 `cookies.json`。调试"明明登录了却说没登录"时先确认用的是哪个文件。
- 评论加载（`feed_detail.go`）是一套刻意模拟人类行为的滚动/点击状态机（随机 sleep、滚动速度档位、停滞检测、最后冲刺），改动时保留这些节奏，否则容易被判定为机器人。

## 本机特有（未提交到仓库）

- `xhs-mcp.exe` 是本机构建产物；`start-xhs-mcp.bat` / `setup-autostart.ps1` / `create-shortcut.ps1` 让它开机自启，**跑在 `:3001` 而不是默认的 `:18060`**。这几个脚本里硬编码的路径是 `C:\jundalou\dev\xiaohongshu-mcp`，和当前仓库位置不一致，改动前先确认。
- `skills/post-to-xhs/` 是一套独立的 Python 发布链路（CDP 直连 Chrome，`scripts/cdp_publish.py`），不走本仓库的 Go 服务，两者互不依赖。
- `rag_out/` 是抓取数据与分析脚本的暂存目录。
