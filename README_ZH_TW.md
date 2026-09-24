# ccusage_go

<p align="center">
  <strong>🚀 高效能的 Claude Code 使用量分析工具 Go 實作版</strong>
</p>

<p align="center">
  <a href="#安裝">安裝</a> •
  <a href="#使用方法">使用方法</a> •
  <a href="#功能特色">功能特色</a> •
  <a href="#功能比較">功能比較</a> •
  <a href="README.md">English</a>
</p>

---

![即時 Token 使用量監控](docs/images/blocks-live-monitor.png)
*即時 token 使用量監控與漸變進度條*

## 關於專案

`ccusage_go` 是 [@ryoppippi](https://github.com/ryoppippi) 開發的熱門工具 [ccusage](https://github.com/ryoppippi/ccusage) 的 Go 語言實作版本。此儲存庫以 [SDpower/ccusage_go](https://github.com/SDpower/ccusage_go) 為基礎，新增多來源報表並保留即時監控。

## 為什麼選擇 Go 版本？

- 原生執行檔，無需 Node.js 執行環境。
- 串流 JSONL 與唯讀 SQLite 解析。
- 內嵌模型定價，支援離線報表。
- 保留 CSV 匯出與即時終端監控。

舊版的下載大小與 TypeScript 效能比較來自僅支援 Claude 的版本；
新版內嵌 SQLite 與更多定價資料，不再沿用那些測量結果。

## 安裝

### 快速安裝（Linux / macOS）

```sh
curl -fsSL https://github.com/RedwindA/ccusage_go/releases/latest/download/install.sh | sh
```

腳本自動辨識 Linux/macOS 與 amd64/arm64，驗證 SHA-256，預設安裝至
`~/.local/bin/ccusage_go`，不需要 sudo。若尚未加入 PATH：

```sh
export PATH="$HOME/.local/bin:$PATH"
ccusage_go --version
ccusage_go daily --offline
```

再次執行即可更新。指定版本或安裝目錄：

```sh
curl -fsSL https://github.com/RedwindA/ccusage_go/releases/download/v0.17.0/install.sh -o install.sh
INSTALL_DIR="$HOME/bin" sh install.sh v0.17.0
```

### 預編譯版本

從 [GitHub Releases](https://github.com/RedwindA/ccusage_go/releases) 下載。
支援 Linux、macOS、Windows 的 amd64 與 arm64，附 `checksums.txt`。
Windows 請解壓縮對應 ZIP，將執行檔改名為 `ccusage_go.exe`。

### 從原始碼編譯

```sh
git clone https://github.com/RedwindA/ccusage_go.git
cd ccusage_go
make build
./bin/ccusage_go --help
```

也可使用 `go install github.com/RedwindA/ccusage_go/cmd/ccusage@latest`，
在 Go bin 目錄安裝名稱為 `ccusage` 的指令；與預編譯的 `ccusage_go` 功能相同。

發版步驟請見 [RELEASING.md](docs/RELEASING.md)。

## 使用方法

### 基本指令

```bash
# 每日使用報告
./ccusage_go daily

# 月度總結
./ccusage_go monthly

# 依對話分析
./ccusage_go session

# 依 session name 查詢
./ccusage_go session --session-name my-feature

# 依 session ID 查詢
./ccusage_go session --session-id ca81db6e-cb9b-4b53-995b-f5d58b0e52f1

# 5 小時計費區塊
./ccusage_go blocks

# 即時監控（含漸變進度條！）
./ccusage_go blocks --live
```

### 進階選項

```bash
# 依日期範圍過濾
./ccusage_go daily --since 2025-01-01 --until 2025-01-31

# 不同輸出格式
./ccusage_go monthly --format json
./ccusage_go session --format csv

# 自訂時區
./ccusage_go daily --timezone Asia/Taipei

# 只顯示最近活動
./ccusage_go blocks --recent
```

## 功能特色

### ✅ 已實作功能

- 💰 **詳細費用明細**：每列報表顯示 API Cost、Cache Create Cost (CC Cost)、Cache Read Cost (CR Cost) 與 Total Cost
- 📊 **每日報告**：每天的 token 使用量和成本
- 📈 **月度報告**：彙總的月度統計資料  
- 💬 **對話分析**：依對話階段的使用量
- ⏱️ **計費區塊**：5 小時計費視窗追蹤
- 🔴 **即時監控**：具有漸變進度條的即時使用量儀表板
- 📊 **使用量配額**：即時顯示 Claude API 配額（session/每週限額）
- 🔄 **Token 自動更新**：OAuth token 過期或收到 401 時自動 refresh，跨平台 credential 儲存（macOS Keychain / Linux & Windows 檔案）
- 🎨 **多種輸出格式**：表格（預設）、JSON、CSV
- 🌍 **時區支援**：可配置報告時區
- 💾 **離線模式**：無需網路連線即可運作
- 🚀 **並行處理**：使用 goroutines 快速載入資料
- 🎯 **記憶體效率**：串流式 JSONL 處理

### 🎨 視覺增強（Go 版獨有）

- **漸變進度條**：在 LUV 色彩空間中平滑的顏色過渡
- **增強的 TUI**：使用 Bubble Tea 框架建置
- **效能快取**：透過顏色快取優化渲染
- **ccusage 表格版面**：主要報表採用圓角標題、藍色表頭、模型分行與黃色合計列
- **統一 Model 標籤**：支援最新的 Claude 模型格式（Opus-4.6, Sonnet-4.6, Opus-4.5, Sonnet-4.5, Haiku-4.5）

## 功能比較

目前以本機 Rust 版 ccusage 為參考，支援 18 種來源、統一報表、statusline、
專案分組、模型與工作區報表、設定檔和自訂定價。
請參閱下方「統一多來源報表」。

日報、週報、月報、會話、模型與工作區報表使用 ccusage 的終端表格風格，
Token 數字加上千位分隔，美元金額保留兩位小數。標準表格顯示輸入、輸出、
快取建立、快取讀取、Token 合計及總費用；原有 API/CC/CR 費用欄改為只顯示總費用。
`--by-agent --breakdown` 可顯示來源與模型明細。

表格依終端寬度調整，重新導向時預設為 120 欄；可用 `COLUMNS=80` 指定寬度。
小於 100 欄或指定 `--compact` 時隱藏快取與 Token 合計欄，
`--responsive=false` 保留完整自然寬度。極窄終端仍保留可讀的最小欄寬，可能需要橫向捲動。
`--no-color`／`NO_COLOR` 停用顏色，`--color`／`FORCE_COLOR` 可強制啟用。
JSON 和 CSV 的資料格式維持不變。

## 技術堆疊

- **程式語言**：Go 1.23+
- **CLI 框架**：[Cobra](https://github.com/spf13/cobra)
- **TUI 框架**：[Bubble Tea](https://github.com/charmbracelet/bubbletea)
- **表格渲染**：[tablewriter](https://github.com/olekukonko/tablewriter)
- **樣式處理**：[Lip Gloss](https://github.com/charmbracelet/lipgloss)
- **顏色漸變**：[go-colorful](https://github.com/lucasb-eyer/go-colorful)

## 開發

### 先決條件

- Go 1.23 或更高版本
- Make（選擇性，為了方便）

### 建置

```bash
# 基本建置
make build

# 為所有平台建置
make build-all

# 執行測試
make test

# 啟用效能分析執行
ENABLE_PROFILING=1 go test -v ./...
```

### 專案結構

```
ccusage_go/
├── cmd/ccusage/        # CLI 進入點
├── internal/           # 核心實作
│   ├── calculator/     # 成本計算邏輯
│   ├── commands/       # CLI 指令處理器
│   ├── loader/         # 資料載入和解析
│   ├── monitor/        # 即時監控功能
│   ├── output/         # 格式化和顯示
│   ├── pricing/        # 價格取得和快取
│   ├── types/          # 類型定義
│   └── usage/          # Claude API 使用量配額
├── docs/               # 文件
└── test_data/          # 測試資料
```

## 效能建議

1. **大型資料集**：Go 版本使用串流和並行處理以達到最佳效能
2. **記憶體優化**：實作智慧型檔案過濾，只載入 12 小時內活動的專案
3. **即時監控**：漸變計算已快取以確保流暢的即時更新
4. **資源使用**：blocks --live 模式僅使用 ~54MB 記憶體，對系統幾乎無影響
5. **增量快取**：live 模式的專案層級快取在資料未變動時降低 68% CPU 使用率

## 致謝

- 🙏 原始 [ccusage](https://github.com/ryoppippi/ccusage) 作者 [@ryoppippi](https://github.com/ryoppippi)
- 🎨 [Bubble Tea](https://github.com/charmbracelet/bubbletea) 提供的精美 TUI 框架
- 💙 所有貢獻者和使用者

## 授權

[MIT](LICENSE) © [@SteveLuo](https://github.com/sdpower)

## 貢獻

歡迎貢獻！請隨時提交 Pull Request。

## 開發路線圖

- [ ] 實作剩餘的 TypeScript 功能
- [ ] 為主要平台新增預編譯版本
- [ ] 新增更多自訂選項
- [x] 實作 `--project` 和 `--instances` 過濾器
- [ ] 新增國際化支援

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=RedwindA/ccusage_go&type=Date)](https://star-history.com/#RedwindA/ccusage_go&Date)

---

<p align="center">
  使用 Go 語言 ❤️ 打造
</p>

## 統一多來源報表

新版命令對齊本機 Rust 版 ccusage 的使用方式：`ccusage daily` 彙整所有偵測到的
來源，`ccusage claude daily` 或 `ccusage codex daily` 則只查詢指定來源。
支援 Claude、Codex、OpenCode、Amp、Droid、Codebuff、Hermes、pi、Goose、
OpenClaw、Kilo、Kimi、Qwen、Copilot、Gemini、Antigravity、Grok、ZCode。

```bash
ccusage daily --offline --last 7 --by-agent
ccusage daily --sections monthly,session --json --no-cost
ccusage claude daily --instances --project myproject
ccusage claude workspace --breakdown
ccusage codex model --speed fast
ccusage blocks --json --offline
ccusage statusline --cost-source both
```

日期篩選支援 `YYYY-MM-DD` 與 `YYYYMMDD`，並在指定時區依日、週、月分組。
`--last N` 包含目前所在的曆日、週或月，不能與 `--since` / `--until` 同時使用。
設定檔支援共用預設值、命令設定、自訂模型定價和具名 pi 儲存區，命令列參數優先。
保留 Go 版 CSV、會話名稱篩選和即時監控。
