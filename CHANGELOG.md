# Changelog

All notable changes to Sentinel-OS are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [v1.1.0] — 2026-08-01

### 🆕 Added
- **Multi-Provider AI Integration** — Sentinel-OS now supports 3 AI backends:
  - **Google Gemini** (Cloud) — existing support improved
  - **Ollama** (Local, 100% free) — runs on your own GPU via the Ollama HTTP API
  - **Nvidia NIM API** (Cloud) — access to DeepSeek V4 Pro and other Nvidia-hosted models via `build.nvidia.com`
- **Debug Logger UI** (`🪵 Logs` tab) — live in-app log viewer showing every engine event, HTTP request, response, and error
  - Backed by a **fixed-size ring buffer** (max 500 entries) — guaranteed zero memory growth
  - Polls the backend every 2 seconds **only** while the Logs tab is active — no background CPU waste
  - Supports **level filtering** (ALL / DEBUG / INFO / WARN / ERROR)
  - Shows **stats bar** (total, per-level counts), **auto-scroll**, **Copy All**, and **Clear** controls
  - Log file also saved to `./logs/sentinel-os-YYYY-MM-DD.log` next to the executable
- **Full action logging** — every significant user action is now logged:
  - Snapshot create, diff, restore, delete
  - AI commit message generation and AI review (with HTTP request/response tracing)
  - Pomodoro start, complete, cancel, distraction alert
  - Watcher rule add/remove
- **Pomodoro Whitelist & Blacklist** — allow/block specific running apps during a focus session
  - Uses a live dropdown populated from the Windows API (no manual typing)
  - Shows a custom in-app popup alert (not a native Windows dialog) for violations
  - Plays a notification tone and sends a system notification when the timer completes
- **AI Review Prompt Improvements** — enforces `<p>` tag wrapping for proper line spacing between Summary, Syntax Check, and Logic Explanation sections
- **HTTP timeout** on Nvidia API calls (60s) — prevents the UI from hanging indefinitely on slow responses
- **Diff truncation** — diffs larger than 4,000 characters are automatically truncated before being sent to any AI provider to prevent API timeouts and rate-limit errors

### 🔧 Changed
- **Settings UI** now shows provider-specific fields dynamically:
  - Gemini: API key
  - Ollama: model name (defaults to `llama3`)
  - Nvidia: API key + model name (defaults to `deepseek-ai/deepseek-v4-pro`)
- **Syntax Check section** in AI reviews now outputs a concise one-line status instead of verbose prose
- Removed unused `internal/guardian` package (backup engine replaced by Snapshots)

### 🐛 Fixed
- Snapshot diff view no longer shows full file contents — only changed chunks with 2-3 lines of surrounding context
- Commit message auto-suggestion now triggers correctly on snapshot creation
- Settings page correctly saves and restores all AI provider fields on reload

---

## [v1.0.0] — 2026-07-27 — Initial Release

### 🆕 Added
- **Pomodoro Focus Timer** with session history
- **Watcher (Productivity Telemetry)** — active window tracking and categorization
- **System Resources Dashboard** — CPU, RAM, Disk, Network, Top Processes
- **Sentinel (Uptime Monitor)** — TCP/HTTP target monitoring with latency tracking
- **Snapshots (Deterministic Version Control)** — local Git-free version history with line-level diffs
- **Google Gemini AI integration** — auto commit message generation and code review
- **Command Palette** (`Ctrl+K`) — app-wide instant navigation
