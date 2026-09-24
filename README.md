# ccusage_go

<p align="center">
  <strong>Usage and cost reports for Claude Code, Codex, and other coding agents</strong>
</p>

<p align="center">
  <a href="#installation">Installation</a> •
  <a href="#usage">Usage</a> •
  <a href="#features">Features</a> •
  <a href="#comparison">Comparison</a> •
  <a href="README_ZH_TW.md">繁體中文</a>
</p>

---

![Live Token Usage Monitor](docs/images/blocks-live-monitor.png)
*Real-time token usage monitoring with gradient progress bars*

## About

`ccusage_go` is a Go implementation of the popular [ccusage](https://github.com/ryoppippi/ccusage) tool by [@ryoppippi](https://github.com/ryoppippi). This repository builds on [SDpower/ccusage_go](https://github.com/SDpower/ccusage_go), adding unified reports for multiple coding agents while retaining Go-specific reporting and live monitoring features.

## Why Go Version?

- A native binary with no Node.js runtime requirement.
- Streaming JSONL and read-only SQLite support for local agent history.
- Embedded model pricing for reproducible offline reports.
- Go-specific CSV export and a live terminal dashboard.

The previous download-size and TypeScript performance comparisons described an
older Claude-only release. They do not describe the current binary, which also
embeds SQLite and expanded pricing catalogs.

## Installation

### Quick Install (Linux / macOS)

```sh
curl -fsSL https://github.com/RedwindA/ccusage_go/releases/latest/download/install.sh | sh
```

The installer detects Linux/macOS and amd64/arm64, verifies the release archive's
SHA-256 checksum, and installs `ccusage_go` into `~/.local/bin` without sudo.
Add that directory to your PATH if needed:

```sh
export PATH="$HOME/.local/bin:$PATH"
ccusage_go --version
ccusage_go daily --offline
```

Run the same command to upgrade. To choose a version or installation directory:

```sh
curl -fsSL https://github.com/RedwindA/ccusage_go/releases/download/v0.17.0/install.sh -o install.sh
INSTALL_DIR="$HOME/bin" sh install.sh v0.17.0
```

### Pre-built Binaries

[GitHub Releases](https://github.com/RedwindA/ccusage_go/releases) includes Linux,
macOS, and Windows archives for amd64 and arm64, plus `checksums.txt`.
On Windows, extract the matching ZIP and rename the executable to `ccusage_go.exe`.

### From Source

```sh
git clone https://github.com/RedwindA/ccusage_go.git
cd ccusage_go
make build
./bin/ccusage_go --help
```

Alternatively, `go install github.com/RedwindA/ccusage_go/cmd/ccusage@latest`
installs the command as `ccusage` in your Go bin directory. In this README,
`ccusage_go` refers to the pre-built binary; both commands support the same options.

## Usage

### Basic Commands

```bash
# Daily usage report
./ccusage_go daily

# Monthly summary
./ccusage_go monthly

# Session-based analysis
./ccusage_go session

# Query specific session by name
./ccusage_go session --session-name my-feature

# Query specific session by ID
./ccusage_go session --session-id ca81db6e-cb9b-4b53-995b-f5d58b0e52f1

# 5-hour billing blocks
./ccusage_go blocks

# Live monitoring (with gradient progress bars!)
./ccusage_go blocks --live
```

### Advanced Options

```bash
# Filter by date range
./ccusage_go daily --since 2025-01-01 --until 2025-01-31

# Different output formats
./ccusage_go monthly --format json
./ccusage_go session --format csv

# Custom timezone
./ccusage_go daily --timezone America/New_York

# Show only recent activity
./ccusage_go blocks --recent
```

## Features

### ✅ Implemented Features

- 💰 **Detailed Cost Breakdown**: API Cost, Cache Create Cost (CC Cost), Cache Read Cost (CR Cost), and Total Cost per report row
- 📊 **Daily Reports**: Token usage and costs per day
- 📈 **Monthly Reports**: Aggregated monthly statistics  
- 💬 **Session Analysis**: Usage by conversation session
- ⏱️ **Billing Blocks**: 5-hour billing window tracking
- 🔴 **Live Monitoring**: Real-time usage dashboard with gradient progress bars
- 📊 **Usage Limits**: Live display of Claude API quota (session/weekly limits)
- 🔄 **Auto Token Refresh**: Automatic OAuth token refresh on expiry or 401, with cross-platform credential storage (macOS Keychain / Linux & Windows file)
- 🎨 **Multiple Output Formats**: Table (default), JSON, CSV
- 🌍 **Timezone Support**: Configurable timezone for reports
- 💾 **Offline Mode**: Works without internet connection
- 🚀 **Parallel Processing**: Fast data loading with goroutines
- 🎯 **Memory Efficient**: Streaming JSONL processing

### 🎨 Visual Enhancements (Go Exclusive)

- **Gradient Progress Bars**: Smooth color transitions in LUV color space
- **Enhanced TUI**: Built with Bubble Tea framework
- **Performance Caching**: Optimized rendering with color caching
- **"WITH GO" Branding**: All reports clearly marked as Go version
- **Unified Model Labels**: Support for latest Claude model formats (Opus-4.6, Sonnet-4.6, Opus-4.5, Sonnet-4.5, Haiku-4.5)

## Unified reports and source commands

Running `ccusage_go`
without a subcommand produces a daily report across detected sources. Use a source
prefix to focus on one agent:

```bash
ccusage_go daily --offline --last 7 --by-agent
ccusage_go daily --sections monthly,session --json --no-cost
ccusage_go claude daily --project myproject --instances
ccusage_go claude workspace --breakdown --since 2026-01-01
ccusage_go codex model --speed fast --offline
ccusage_go codex session --json
ccusage_go pi daily --pi-path /path/to/sessions,/archive/sessions
ccusage_go blocks --json --offline
ccusage_go statusline --cost-source both
```

Supported sources: Claude Code, Codex, OpenCode, Amp, Droid, Codebuff, Hermes,
pi-agent, Goose, OpenClaw, Kilo, Kimi, Qwen, GitHub Copilot CLI, Gemini CLI,
Antigravity, Grok Build CLI, and ZCode. SQLite stores are read without a separate
SQLite installation. Existing Go CSV output, session-name filters, and live
monitoring remain available.

Daily, weekly, and monthly reports share inclusive `--since`/`--until` filters
(YYYY-MM-DD or YYYYMMDD), timezone-aware grouping, `--last N`, sorting, compact
output, JSON, CSV, and model breakdowns. `--last` counts calendar periods including
the current period and cannot be combined with explicit date bounds. Claude,
Codex, and Droid also provide `model` and `workspace` reports. Workspace grouping
uses the full recorded workspace path.

Terminal reports follow ccusage's boxed layout: a rounded title, blue column
headers, multiline model lists, comma-separated token counts, two-decimal USD
costs, and yellow totals. `--by-agent --breakdown` adds agent and model detail
rows. Standard tables show input, output, cache creation, cache reads, total
tokens, and total cost; the former API/CC/CR cost columns are omitted to match
ccusage's layout.

Tables fit the detected terminal width (120 columns when redirected).
`COLUMNS=80 ccusage_go daily --offline` selects a reproducible narrow layout.
Below 100 columns, or with `--compact`, cache and total-token columns are hidden;
`--responsive=false` keeps the full natural-width layout. Very narrow terminals
retain readable minimum column widths and may need horizontal scrolling.
`--no-color` or `NO_COLOR` disables colors; `--color` or `FORCE_COLOR` enables
them for redirected output. JSON and CSV exports retain their existing schemas.

`--mode auto` uses available recorded costs, `--mode calculate` recalculates them,
and `--mode display` uses recorded costs only, with source-specific billing rules.
`--offline` uses embedded pricing snapshots; `--no-offline` enables online refresh.
Custom per-model overrides are supported in JSON configuration. `--no-cost`
removes cost columns and JSON fields, including nested breakdowns. `--jq` pipes
JSON through an installed `jq` executable.

Configuration is discovered in `.ccusage/ccusage.json`, then the Claude config
locations, or selected with `--config`. Explicit CLI arguments take precedence.
For example:

```json
{
  "defaults": {"offline": true, "timezone": "UTC"},
  "commands": {"daily": {"breakdown": true}},
  "pi": {"stores": [{"name": "archive", "path": "/data/pi-archive"}]}
}
```

Named pi stores contribute to unified reports and must not overlap other pi
stores. On Linux, root can use `--all-users` to scan system accounts' default
stores grouped by username; this ignores custom source paths and named stores.


## Technical Stack

- **Language**: Go 1.23+
- **CLI Framework**: [Cobra](https://github.com/spf13/cobra)
- **TUI Framework**: [Bubble Tea](https://github.com/charmbracelet/bubbletea)
- **Table Rendering**: [tablewriter](https://github.com/olekukonko/tablewriter)
- **Styling**: [Lip Gloss](https://github.com/charmbracelet/lipgloss)
- **Color Gradients**: [go-colorful](https://github.com/lucasb-eyer/go-colorful)

## Releasing

See [RELEASING.md](docs/RELEASING.md) for the tag-based release process.

## Development

### Prerequisites

- Go 1.23 or higher
- Make (optional, for convenience)

### Building

```bash
# Basic build
make build

# Build for all platforms
make build-all

# Run tests
make test

# Run with profiling
ENABLE_PROFILING=1 go test -v ./...
```

### Project Structure

```
ccusage_go/
├── cmd/ccusage/        # CLI entry point
├── internal/           # Core implementation
│   ├── calculator/     # Cost calculation logic
│   ├── commands/       # CLI command handlers
│   ├── loader/         # Data loading and parsing
│   ├── monitor/        # Live monitoring features
│   ├── output/         # Formatting and display
│   ├── pricing/        # Price fetching and caching
│   ├── types/          # Type definitions
│   └── usage/          # Claude API usage limits
├── docs/               # Documentation
└── test_data/          # Test fixtures
```

## Performance Tips

1. **Large Datasets**: The Go version uses streaming and parallel processing for optimal performance
2. **Memory Optimization**: Implements smart file filtering, only loading projects active within 12 hours
3. **Live Monitoring**: Gradient calculations are cached for smooth real-time updates
4. **Resource Usage**: blocks --live mode uses only ~54MB memory with minimal system impact
5. **Incremental Cache**: Project-level caching in live mode reduces CPU usage by 68% when data is unchanged

## Acknowledgments

- 🙏 Original [ccusage](https://github.com/ryoppippi/ccusage) by [@ryoppippi](https://github.com/ryoppippi)
- 🎨 [Bubble Tea](https://github.com/charmbracelet/bubbletea) for the beautiful TUI framework
- 💙 All contributors and users

## License

[MIT](LICENSE) © [@SteveLuo](https://github.com/sdpower)

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## Roadmap

- [x] Add unified reports and adapters for multiple coding agents
- [x] Add pre-built binaries for major platforms
- [ ] Add more customization options
- [x] Implement `--project` and `--instances` filters
- [ ] Add internationalization support

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=RedwindA/ccusage_go&type=Date)](https://star-history.com/#RedwindA/ccusage_go&Date)

---

<p align="center">
  Made with ❤️ in Go
</p>
