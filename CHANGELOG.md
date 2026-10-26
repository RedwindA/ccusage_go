# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

## [v0.20.0] - 2026-10-25

This release ports every upstream ccusage change from v20.0.24 through v20.0.44. On real Claude Code and Codex history, its daily, weekly, monthly and session totals now match ccusage v20.0.44 exactly.

### Added

- Codex Flex pricing: rollouts that record `service_tier = "flex"` bill at the model's Flex rate (half price for the GPT-5 and GPT-6 families, o3 and o4-mini), `--speed flex` forces it, and `pricingOverrides` accepts `flexMultiplier`.
- `codex session --id` accepts the full session ID, the rollout file name (with or without `.jsonl` or `.jsonl.zst`), the bare thread UUID or a `codex://threads/<uuid>` link; `--json` prints that one session with its `totalCost`, and an unknown, empty or ambiguous ID exits with an error.
- In the default `auto` cost mode, JSON `modelBreakdowns` entries include `storedCost`, the part of `cost` taken from costs recorded in agent logs (such as Claude's `costUSD`). It is omitted when no recorded cost was used, in `--mode calculate` and `--mode display`, and with `--no-cost`.
- Antigravity conversations under `~/.gemini/antigravity-acp/conversations/` (ACP mode) are discovered by default.

### Changed

- Embedded pricing is refreshed to the LiteLLM and models.dev snapshots of ccusage v20.0.44, so Claude Opus, Sonnet and Haiku 5.5, GPT-6 Sol, GPT-6.1 Sol, GPT-6 Luna, Gemini 4 Argon, o3 and o4-mini are priced offline. Previously Opus 5.5 was billed at Opus 5 rates and the others cost $0.
- Model IDs are matched to prices the way ccusage does: LiteLLM and built-in rates come before models.dev, and fuzzy matching never crosses version numbers, so an unknown version such as `claude-opus-4-9` is reported as missing pricing instead of being billed as `claude-opus-4`. Online runs price models that LiteLLM lacks from the live models.dev catalog before the embedded snapshot.
- Unified reports apply each source's own config (`<source>.defaults`, `<source>.commands.<report>` and `commands."<source>:<report>"`) to that source: date window, timezone, cost mode, offline setting, pricing overrides, Codex speed, pi and OpenClaw paths and Claude `dataPath`. Explicit command-line flags still take precedence.
- Config follows ccusage's rules: `project`, `instances` and `projectAliases` apply only to `claude daily`, and unified `daily` passes `project` on to Claude rows only. `last`, `date`, `month`, `week`, `id` and `sessionId` are ignored in config files. Top-level `blocks` and `statusline` no longer read Claude config sections.
- A set `CLAUDE_CONFIG_DIR` names only directories that hold `projects/` (or `projects/` directories themselves). When no entry qualifies, reports that read Claude fail with "no valid Claude data directories found in CLAUDE_CONFIG_DIR" instead of quietly leaving Claude out.
- A unified report fails with a source's error when that source cannot be loaded at all, instead of warning and totaling the rest. Individual files that cannot be read or parsed are skipped, as ccusage skips them.
- `daily`, `monthly` and `session` no longer accept `-w`/`--start-of-week` or read `startOfWeek`; `weekly` and the source reports keep it. `--last` can no longer be combined with `--sections`.
- With `--sections`, tables print in a fixed order: the command's own table, then daily, weekly, monthly and session. Each section sorts like its standalone report. Empty reports still print their title, and no-data messages go to stderr.
- The statusline shows the block's remaining time as `(2h 13m left)`. It reads only recent files to find today's cost and the active block, and it sizes the context window as ccusage does (for example 272K for `gpt-5.2`).
- Codex tables show a Reasoning column and keep every column on narrow terminals and with `--compact`.
- Date-bounded Claude reports (`--since`, `--last` and similar) skip sessions last written more than a day before the window. Output is unchanged.
- Release binaries require macOS 12 or later, and building from source requires Go 1.25 or later.

### Fixed

- Codex reports read the `.jsonl.zst` rollouts that Codex has compressed older sessions into since 2026-10-14; before, those sessions were silently left out. A plain copy wins over a compressed copy of the same rollout, and `sessions/` still wins over `archived_sessions/`.
- Codex reports count automatic compaction usage recorded only in `token_usage_record` lines. They also count once the usage that a branch rollout copies from its parent.
- Codex: an unselected `config.toml` profile no longer triggers Fast pricing. Dated and provider-qualified ids (`gpt-5.4-2026-03-05`, `global.openai.gpt-5.5`) get their base model's Fast multiplier, and GPT-6 Sol, GPT-6.1 Sol and GPT-6 Luna Fast usage bills at 2×.
- Codex bills cached input at the input rate for models without a cache-read price, such as gpt-5-pro, o3-pro, gpt-4 and grok-4.3. Codex usage is priced in `--mode display`, is priced under its `CCUSAGE_MODEL_ALIASES` name, counts recorded `totalTokens`, and reads token counts written as strings.
- Claude: gateways that reuse one message ID without a request ID have each response counted. Anthropic `msg_` and OpenAI `resp_` content blocks, same-instant duplicates and `/btw` sidechain replays still count once, and daily, weekly, monthly and session totals agree.
- Claude usage is attributed to the line's `sessionId` rather than `session_id`. Workflow agents under `subagents/workflows/` are listed as their own sessions, and a project or session found under two Claude roots is shown as one row.
- `claude daily` and unified daily, weekly and monthly reports count subagent calls recorded only as AgentProgress lines, once each.
- Claude advisor calls are no longer billed a second time for their parent's cache writes, and a fast advisor call is listed under the advisor's model. Cache-write tokens come from the `cache_creation` 5m/1h breakdown when present, and `<synthetic>` entries count toward totals without a model.
- Claude lines that ccusage rejects are skipped: a null required field, a field of the wrong type, a fractional token count, an unknown speed, or a malformed timestamp (including offsets past ±23:59).
- Claude model IDs ending in `-fast` (such as Copilot's `claude-opus-4.6-fast`) are no longer charged the Fast multiplier unless the usage records fast speed. Priority Tier requests bill at the standard rate.
- Filtering Claude by project counts requests whose deduplicated copy was recorded in another project. `--project` reads a symlinked project directory, and symlinked transcripts inside `projects/` are skipped, as ccusage does.
- Source directories that are symlinks or Windows junctions (`~/.claude/projects`, `$CODEX_HOME/sessions`, `PI_AGENT_DIR`, and the Copilot, OpenCode, Codebuff, Kimi and Antigravity roots) are read again instead of reporting no usage.
- Claude Sonnet 4 requests above 200K tokens bill only the tokens past 200K at the long-context rate. A pricing override for an id with no price of its own (such as `claude-sonnet-4-5-corp`) starts from no rates. Overrides also apply to provider-prefixed, dated or differently separated variants of the overridden name. DeepSeek V4 time-of-day rates apply only to the lowercase ids, as in ccusage.
- Copilot: reports count OpenTelemetry calls missing from shutdown summaries, count re-exported spans once and exclude cache writes from input tokens. Session credits missing from the per-model breakdown are reported as cost-only `unknown` rows at $0.01 per AI credit.
- OpenCode forks no longer count history copied from the parent session, and `--mode display` shows only recorded OpenCode costs.
- Antigravity: empty conversation databases are skipped, and default roots read only their `conversations/` folder, so a fresh install with only `conversation_summaries.db` no longer fails. Model IDs 1417–1419, 1050 and 1402 are reported as `gemini-4-argon`, `gemini-3.1-flash-lite` and `claude-opus-5-5`.
- pi: `pricingOverrides` entries (keyed `[pi] model`, `[omp] model`, by the bare model name or through an alias) apply in `auto` mode even when pi recorded a cost, and a negative recorded cost is priced from tokens.
- Hermes normalizes billing provider names as ccusage does and falls back to the model's own rate when the provider rate is free.
- ZCode prices current Z.ai plan provider IDs (`account:zai-*`, `builtin:zai-*`) and skips databases without the session schema.
- Gemini CLI usage is priced under `google/`, `gemini/`, `vertex_ai/` and `openrouter/google/` ids first. A message re-recorded with zero tokens keeps its usage, and thoughts that the recorded total already covers are not added again.
- Droid, Codebuff and Goose are priced in `--mode display` and fall back to provider-qualified model ids. Hyphenated Droid `providerLock` values are recognized, and Codebuff and Copilot read the real count when one token alias is zero.
- Kimi and OpenClaw no longer bill the surplus of a recorded total as output. OpenClaw messages with a null timestamp are dated by their record, and Kilo uses provider-qualified rates only when they are listed exactly.
- `blocks` starts blocks on the UTC hour and lists them in start-time order. It takes the default token limit from the blocks shown and rounds the active block's projection as ccusage does. It also honors `NO_COLOR` and redirected output, lists `-fast` and aliased model names, reports `usageLimitResetTime`, and aligns its title boxes.
- Unified reports keep rows that have a cost but no tokens, drop agents without usage on a day, and bound unified `session` reports by the UTC date of each session's last activity.
- `--timezone local`, timezone names in any case, POSIX `TZ` rules such as `TZ=CST-8`, and `TZ` on Windows all work. Windows binaries embed time zone data, so named zones no longer fail there.
- `monitor` lists the five newest calls under Recent Activity, in local time.

## [v0.19.0] - 2026-10-25

### Added

- Windows installer: `irm https://github.com/RedwindA/ccusage_go/releases/latest/download/install.ps1 | iex` verifies the release's SHA-256 checksum, installs `ccusage_go.exe` into `%LOCALAPPDATA%\Programs\ccusage_go` without administrator rights, and adds it to the user PATH. Running `install.sh` from Git Bash now points to it.

### Fixed

- `ccusage_go update` on Windows no longer fails when an earlier update's old binary is still running, such as a `blocks --live` session, and the old binary left beside the new one is deleted on the next run instead of lingering until the next update.

## [v0.18.0] - 2026-09-27

### Added

- `ccusage_go update` (alias `upgrade`) upgrades in place from GitHub Releases: it verifies the archive's SHA-256 checksum and that the new binary runs before replacing the current one. `--check` only reports whether an update is available; `ccusage_go update vX.Y.Z` installs a specific release.

### Changed

- Reports are about ten times faster with identical output: `daily` drops from about 1.1s to 0.1s online and from 0.42s to 0.04s offline. Transcripts are scanned without decoding unused text, files are parsed in parallel, and online price lists download while usage loads, with parsed tables cached between runs.

## [v0.17.1] - 2026-09-26

### Fixed

- Online reports no longer stall for several seconds while merging models.dev pricing; `ccusage_go daily` drops from about 5s to under 1s.

## [v0.17.0] - 2026-09-25

### Added

- Shell completion for flag values: output formats, sort order, cost modes, pricing tiers, weekdays, comma-separated `--sections`, statusline options, `--token-limit max`, directory flags, and JSON `--config` files.

### Fixed

- `ccusage_go completion <shell>` scripts now register for the installed `ccusage_go` binary instead of `ccusage`; help output uses the same name.

## [v0.16.0] - 2026-09-25

### Added

- ccusage-style terminal reports with rounded titles, blue headers, yellow totals, multiline model lists, and agent/model breakdown rows.
- Responsive column widths with Unicode-aware wrapping and truncation, compact layouts below 100 columns, and reproducible sizing through `COLUMNS`.
- Reference snapshots and terminal layout tests covering wide and narrow output, Unicode, colors, exports, and distinct model identities.

### Changed

- Standard report tables show comma-separated token counts and two-decimal USD totals. Separate API/CC/CR cost columns are omitted to match ccusage's layout; compact tables hide cache metrics and total tokens. JSON/CSV schemas remain unchanged.
- Session detail tables respect terminal width and color settings, including `NO_COLOR` and `FORCE_COLOR`.

### Fixed

- Preserve available width for workspace paths, session filenames, and other non-date identifiers instead of applying date-column limits.
- Keep focused model breakdown labels in the Models column so regular and fast variants remain distinguishable.
- Preserve non-date model suffixes such as `kimi-k2-thinking` when shortening model names.
- Keep terminal snapshot fixtures at LF line endings on Windows checkouts.

## [v0.15.0] - 2026-09-21

### Added

- Public releases under `RedwindA/ccusage_go`, with Linux/macOS/Windows amd64 and arm64 archives, SHA-256 checksums, and a verified Linux/macOS installer.
- Tag-triggered release automation gated by tests on Linux, macOS, and Windows.

- Unified daily, weekly, monthly and session commands, plus focused commands for 18 coding-agent sources from the local Rust-first ccusage reference.
- Native JSON/JSONL, SQLite and Antigravity protobuf readers; model and workspace reports for Claude, Codex and Droid.
- Calendar-period filters, project/instance grouping, agent breakdowns, sections, no-cost output, JSON configuration, custom pricing, named pi stores and Linux all-users reports.
- Claude statusline hook with context usage, cost source selection, burn rate and cache invalidation; embedded LiteLLM and models.dev pricing with source rules.

### Fixed

- Windows SQLite file URI handling for native agent stores.

- Consistent date filtering across table, JSON and CSV, including timezone/DST.
- Claude streaming duplicate replacement, sidechain replay and advisor usage; recursive incremental cache discovery and file truncation handling.
- Source-specific cached/reasoning token accounting and cumulative usage replay.
- Clean blocks JSON, fractional billing windows, and propagation of pricing and no-cost settings into live monitoring.

## [v0.14.0] - 2026-05-08

### 🐛 Bug Fixes

- **fix(loader)**：修正 filter 模式（`live monitor` / `--modified-within` / `--only-active`）漏掃 `subagents/` 子目錄
  - 實測差距：21,775 entries、4,711,090 output tokens；sub-agent JSONL 占資料夾 53%
  - `collectProjectFiles` 對名為 `subagents` 的子目錄遞迴一層（新增 `collectSubagentFiles`）
  - `shouldSkipProject` 同步偵測 subagents 內活動，避免整個 project 被誤判為 inactive 而 skip
  - `IncrementalCache.Update` 列舉 + mtime 比對涵蓋 subagents/，live 增量快取相容

### ✨ Features

- **feat(types)**：`UsageEntry` 新增 first-class 欄位
  - `CacheCreationInputTokens` / `CacheReadInputTokens`：取代 `Raw["cache_*"]` 間接存取
  - `IsSidechain` / `IsMeta`：保留對話樹 metadata 供 breakdown 報表使用（不過濾、不影響計費）
  - `WebSearchRequests` / `WebFetchRequests` / `WebSearchCost` / `WebFetchCost`：server_tool_use 計費
  - `CacheMissReason`：新結構，承載 Anthropic `diagnostics.cache_miss_reason` 診斷資料
- **feat(loader)**：nested `usage.cache_creation` fallback
  - 當 flat `cache_creation_input_tokens` 缺值時，讀取 nested `ephemeral_1h_input_tokens + ephemeral_5m_input_tokens` 加總
  - 為 Anthropic 未來移除 flat 欄位預先相容；目前 flat 與 nested 同值，flat 優先
- **feat(pricing)**：server_tool_use 計費機制
  - `PricingService.GetModelPrice` 簽章新增 `webSearchPrice` / `webFetchPrice` 兩個回傳值
  - `ModelPricing` 結構新增 `WebSearchCostPerRequest` / `WebFetchCostPerRequest` 欄位
  - Embedded 預設採 Anthropic 公告值：web_search $0.01/req、web_fetch $0/req（保留升級空間）
- **feat(calculator)**：`calculateSingleCost` 改讀 first-class 欄位，並計入 web_search / web_fetch 費用至 `Cost`（不計入 `APICost`）
- **feat(loader)**：`ai-title` 納入 SessionName 第三順位來源
  - 優先序：custom-title > agent-name > ai-title
- **feat(loader)**：`message.diagnostics.cache_miss_reason` 萃取至 `entry.CacheMissReason`

### 🔧 Refactoring

- **refactor(loader)**：抽出 `clearRawExceptKeys(entry, keep)` helper，統一 stream / 非 stream / project_cache 三處 Raw 清理邏輯，`usage_limit_reset_time` 在所有路徑一致保留
- **refactor(loader)**：`shouldCountAsParseError` 集中宣告 `nonUsageEntryTypes` 白名單，新增 `attachment` / `system` / `last-prompt` / `file-history-snapshot` / `permission-mode` / `agent-setting` / `queue-operation` / `ai-title`，避免合法 type 被誤計入 parseErrors
- **refactor(loader)**：`extractProjectPath` 移除 YYYY/MM/DD dead code 分支與未使用的 `isNumeric`；補上 subagents/ 父目錄 fallback
- **refactor(calculator/output)**：全 repo 移除 `Raw["cache_*"]` 取用，改讀 first-class 欄位（`blocks.go` / `tablewriter_formatter.go` / `calculator.go`）

### ⚠️ Breaking Changes

- **`pricing.PricingService.GetModelPrice` 簽章變更**：回傳值從 4 個 float64 + error 改為 6 個 float64 + error（新增 webSearchPrice、webFetchPrice）
- **`calculator.PricingService` 介面**：同步擴張；任何實作此介面的下游程式需更新

### 🧪 Tests

- 新增 `internal/loader/subagents_test.go`（4 個測試）：filter 模式遞迴、預設模式對齊、IncrementalCache 偵測、shouldSkipProject 偵測
- 新增 `internal/loader/usage_schema_test.go`（11 個測試）：nested cache_creation、ai-title 優先序、usage_limit_reset_time 保留、server_tool_use、IsSidechain、parseError 白名單、CacheMissReason
- 新增 `internal/calculator/calculator_test.go`（3 個測試）：first-class cache 欄位、server_tool_use 費用、SessionReport 聚合
- 既有 `session_report_test.go` 同步遷移至 first-class 欄位

### 📁 Files Changed

- `internal/types/usage.go`
- `internal/loader/loader.go`
- `internal/loader/project_cache.go`
- `internal/pricing/pricing.go`
- `internal/calculator/calculator.go`
- `internal/calculator/blocks.go`
- `internal/output/tablewriter_formatter.go`
- 對應測試檔（calculator_test.go / subagents_test.go / usage_schema_test.go / session_report_test.go）

---

## [v0.13.0] - 2026-04-06

### ✨ Features

- **feat**: OAuth Token 自動 Refresh 機制
  - Token 過期時自動呼叫 `POST platform.claude.com/v1/oauth/token` 換取新 token
  - Usage API 收到 401 時自動 refresh + 重試一次
  - 跨平台 credential 儲存：macOS Keychain / Linux & Windows `.credentials.json`
  - Refresh 後自動寫回 Keychain 或 `.credentials.json`（0600 權限）
  - 併發控制防止同時多次 refresh

### 🐛 Bug Fixes

- **fix**: 補齊 Usage API 缺少的 HTTP headers
  - 新增 `Content-Type: application/json` 和 `User-Agent: claude-code/2.1.92`（實測 4 個 headers 缺一不可）
- **fix**: Token 讀取優先級調整為 env > Keychain > file
  - v2.x 預設使用 macOS Keychain，`.credentials.json` 可能不存在
  - 改為優先讀取 Keychain，再 fallback 到 credentials file

### 📁 Files Changed

- `internal/usage/token.go` — 改造為回傳完整 `*oauthCredential`，新增 `isExpired()`、`getConfigDir()`
- `internal/usage/refresh.go` — 新增 token refresh、credential 寫回（跨平台）、`getValidToken()`、`forceRefreshToken()`
- `internal/usage/usage.go` — 401 自動重試、補齊 4 個必要 headers
- `internal/usage/usage_test.go` — 新增 isExpired、refresh、save、401 重試等 6 個測試

### 🧪 Tests Added

- `TestIsExpired` — 4 子測試（未過期、已過期、剛好過期、無過期時間）
- `TestRefreshCredential` — mock HTTP server 測試 refresh 流程
- `TestRefreshCredentialNoRefreshToken` — 無 refresh token 時的錯誤處理
- `TestRefreshCredentialServerError` — 伺服器錯誤時的處理
- `TestRefreshCredentialKeepsOldRefreshToken` — 伺服器未回傳新 refresh token 時保留舊的
- `TestSaveCredentialToFile` — 寫入 `.credentials.json` 並驗證內容

---

## [v0.12.0] - 2026-03-24

### ✨ Features

- **feat**: 新增 Session Name 顯示與查詢功能
  - 從 JSONL 中解析 `custom-title` 和 `agent-name` 條目取得 session name
  - `session` 命令新增 `--session-id` flag，依 UUID 精確查詢特定 session
  - `session` 命令新增 `--session-name` flag，依 session name 精確查詢
  - Session 報表優先顯示 session name（無 name 時回退為 project path）
  - 跨檔案全局回填 SessionName（subagent 檔案也能正確顯示）
- **feat**: Session 過濾模式新增 Source File 明細
  - `--session-id` / `--session-name` 查詢時，以 Session 為大區塊，逐行列出每個 Source File
  - 每個 Source File 獨立顯示 Models、Input、Output、Cache、Cost、Last Activity
  - 一般模式維持 Files 數字欄不變
- **feat**: Daily/Monthly 報表新增 Sessions 數量欄
  - 統計每個時間區間內的唯一 session 數量
  - Footer 顯示總計唯一 session 數量
- **feat**: Session 報表新增 Session IDs 和 Source Files 追蹤
  - SessionInfo 收集所有唯一 Session UUID 和 Source File 路徑
  - CSV 輸出新增 `session_name`、`session_ids`、`source_files` 欄位
- **feat**: Last Activity 欄顯示日期+時間（當地時區）
- **feat**: 所有報表新增 CC Cost / CR Cost / API Cost 獨立欄位
  - Cache Create Cost 和 Cache Read Cost 分別顯示
  - API Cost 只含 input + output 費用
  - 舊版資料無 cache 時欄位顯示 `-`
- **feat**: Blocks 報表新增 token 和費用明細
  - 從單一 Tokens + Cost 欄拆分為 Input / Output / Cache Create / CC Cost / Cache Read / CR Cost / Total Tokens / API Cost / Cost
  - Gap 行和 REMAINING/PROJECTED 特殊行同步更新

### 📁 Files Changed

- `internal/types/usage.go` — 新增 SessionName、SessionIDs、SourceFiles、SourceFile、SourceFileStat
- `internal/loader/loader.go` — 攔截 custom-title/agent-name、設定 SourceFile、全局 sessionNameMap 回填
- `internal/commands/session.go` — 新增 --session-id/--session-name flags、過濾模式調用 detail 報表
- `internal/commands/shared.go` — 新增 filterEntriesBySessionID/Name 輔助函式
- `internal/calculator/calculator.go` — GenerateSessionReport 收集 SessionName/SessionIDs/SourceFiles、新增 AggregateBySourceFile
- `internal/output/tablewriter_formatter.go` — FormatSessionDetailReport、Session name 顯示、Sessions 欄、Last Activity 含時間和時區
- `internal/output/formatter.go` — CSV 格式加入 session_name/session_ids/source_files
- `internal/types/usage.go` — UsageEntry、SessionInfo、SourceFileStat 新增 CacheCreateCost / CacheReadCost 欄位
- `internal/calculator/calculator.go` — 分別計算 cache create / cache read 費用
- `internal/output/tablewriter_formatter.go` — 報表新增 CC Cost (USD) / CR Cost (USD) / API Cost (USD) 欄位

### 🧪 Tests Added

- `internal/loader/session_name_test.go` — 6 tests (custom-title, agent-name, priority, empty, cross-file, source-file)
- `internal/calculator/session_report_test.go` — 6 tests (session name, multiple, empty, session IDs, source files, aggregate)
- `internal/commands/session_filter_test.go` — 5 tests (ID/name filter, no match, empty)
- `internal/output/session_count_test.go` — 7 tests (session name, session IDs, files column, detail report, CSV, daily/monthly sessions)

---

## [v0.11.1] - 2026-03-15

### ⚡ Performance

- **perf**: Add project-level incremental cache for `blocks --live` mode
  - Tracks file state (ModTime+Size) per project directory
  - Only reloads changed files; skips entirely when no changes detected
  - Per-project deduplication for efficient append-only JSONL handling
  - Full project reload on file deletion for cache consistency

### 📊 Performance Metrics

| Metric | Before | After | Improvement |
|--------|--------|-------|-------------|
| CPU avg (30s idle) | 26.7% | 8.5% | **-68%** |

### 📁 Files Changed

- `internal/loader/project_cache.go` — New incremental cache implementation
- `internal/loader/project_cache_test.go` — 9 unit tests
- `internal/monitor/blocks_live.go` — Integrated incremental cache into tick handler

---

## [v0.11.0] - 2026-03-11

### ✨ Features

- **feat**: Add LIMITS section to `blocks --live` showing Claude API usage quota in real-time
  - Displays session (5-hour) and weekly limits with color-coded progress bars
  - Green (≤60%), yellow (60-90%), red (>90%) indicators
  - Shows reset times in local timezone
  - Graceful degradation when no OAuth token or API unavailable
- **feat**: Add `internal/usage` package for Claude OAuth Usage API integration
  - Cross-platform OAuth token reading (env var, credentials file, macOS Keychain)
  - 5-minute in-memory cache with concurrent access protection

### 🐛 Bug Fixes

- **fix**: Fix model name display for dateless model IDs
  - `claude-opus-4-6` now correctly shows as `Opus-4.6` (was `claude-opus-`)
  - `claude-sonnet-4-6` now correctly shows as `Sonnet-4.6` (was `claude-sonne`)
  - Tightened date regex to require 8-digit dates, preventing false matches

### 📚 Documentation

- Updated README and README_ZH_TW for v0.11.0
- Updated blocks-live implementation docs with LIMITS section
- Updated API integration docs with Claude OAuth Usage API
- Updated live monitor screenshot

---

## [v0.10.1] - 2025-10-16

### ✨ Features

- **feat**: Add support for Haiku 4.5 model
- **feat**: Update pricing for Sonnet 4.5

### 🎨 Style

- **style**: Standardize model display names to use hyphens (e.g., `Sonnet-4.5`)



## [v0.9.0] - 2024-08-24

### 🎉 Major Performance Improvements

#### Memory Optimization
- **90% reduction** in peak memory usage during live monitoring (446MB → 46MB)
- **83% reduction** in steady-state memory usage (263MB → 45MB)
- Implemented single-worker architecture for reduced resource consumption
- Stream processing with immediate memory release after cost calculation
- Smart retention of only essential cache token data

#### Build System Overhaul
- Default static binary compilation with `CGO_ENABLED=0`
- Binary size optimization with `-ldflags="-s -w"` (14MB → 9.6MB)
- Unified naming convention: all binaries now use `ccusage_go` prefix
- Simplified deployment with zero runtime dependencies

### 🔧 Technical Improvements

#### Core Optimizations
- Changed default `maxWorkers` from 5 to 1 for lower CPU usage
- Implemented progressive memory release during file processing
- Fixed token accumulation in live mode with proper cache token handling
- Aligned with TypeScript version's 24-hour retention window

#### Build and CI/CD
- Updated Makefile with static compilation as default
- Enhanced GitHub Actions workflows for new naming convention
- Added `make dynamic` target for optional dynamic linking
- Improved release automation with proper binary naming

### 📊 Performance Metrics

| Metric | Before | After | Improvement |
|--------|--------|-------|-------------|
| Peak Memory | 446 MB | 46 MB | **-90%** |
| Steady Memory | 263 MB | 45 MB | **-83%** |
| Binary Size | 14 MB | 9.6 MB | **-31%** |
| Process Count | 3 | 2 | **-33%** |

### 🐛 Bug Fixes
- Fixed memory leak in stream processing mode
- Corrected cache token accumulation in `IdentifySessionBlocks`
- Fixed process statistics in monitoring script for npm processes
- Resolved Raw data clearing issue that affected cost calculations

### 📚 Documentation
- Updated performance comparison tables with latest measurements
- Added detailed testing methodology documentation
- Enhanced installation instructions for v0.9.0
- Added monitoring script usage examples

### 🔄 Breaking Changes
- Binary output name changed from `ccusage` to `ccusage_go`
- Release artifacts now use underscore naming (e.g., `ccusage_go-linux-amd64`)

### 📦 Dependencies
- No changes to external dependencies
- Maintained compatibility with Go 1.22+

---

## [v0.8.0] - Previous Release

Initial public release with core functionality:
- Daily, monthly, weekly, and session reports
- 5-hour billing blocks tracking
- Live monitoring mode with gradient progress bars
- JSON/CSV/table output formats
- Cross-platform support
