# 🛡️ Sentinel-OS

**Sentinel-OS** is a powerful, locally-hosted productivity and system monitoring dashboard built with [Wails](https://wails.io/) (Go backend + Vanilla Web frontend). It serves as your personal command center, designed to help you stay focused, secure your data, and keep a watchful eye on your system and services.

---
## ✨ Features

- 🎯 **Pomodoro Focus Timer:** A built-in, highly visual Pomodoro timer to manage focus sessions. Tracks your tasks, logs session history, and automatically cycles between focus and break modes with audio cues. Supports **Whitelist & Blacklist** distraction blocking with real-time running app detection.
- 👁️ **Watcher (Productivity Telemetry):** Automatically tracks your active windows and maps them into categories (Productive, Unproductive, Neutral) so you can review where your time is going. Includes an Activity Timeline and app usage breakdown.
- 📊 **System Resources:** Real-time polling of your machine's CPU, Memory, Disk, Network, and Top Processes via a sleek, dynamic UI.
- 📡 **Sentinel (Uptime Monitoring):** Monitor the uptime, latency, and status of your favorite web services and APIs.
- 📸 **Snapshots:** A built-in, Git-free local version control system. Take instant snapshots of your project directories, view line-by-line code diffs, and easily roll back to previous states without managing external repositories.
- 🤖 **Multi-Provider AI Integration:** AI-powered commit message generation and code review with three providers:
  - **Google Gemini** (Cloud API)
  - **Ollama** (Local, completely free)
  - **Nvidia NIM API** (Cloud, access to models like DeepSeek V4 Pro)
- 🪵 **Debug Logger:** A live in-app debug log viewer that shows every action, HTTP request, error, and engine event in real time. Backed by a fixed-size ring buffer (max 500 entries) — zero memory bloat.
- ⌨️ **Command Palette:** Press `Ctrl + K` to quickly navigate anywhere inside the OS.

---

## 🚀 Getting Started

### Quick Download (Recommended)
1. Head over to our **[Releases Page](https://github.com/anandhu-kb/Sentinel-OS/releases)**.
2. Download the latest `Sentinel-OS.exe`.
3. Run it from anywhere on your PC!

### Build from Source
If you prefer to compile it yourself:

1. **Prerequisites:**
   - [Go 1.20+](https://go.dev/doc/install)
   - [Wails v2](https://wails.io/docs/gettingstarted/installation): `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
   - [Node.js 18+](https://nodejs.org/) (for the frontend build step)

2. **Clone and Build:**
   ```bash
   git clone https://github.com/anandhu-kb/Sentinel-OS.git
   cd Sentinel-OS
   wails build
   ```

3. The compiled executable will be at `build/bin/Sentinel-OS.exe`.

---

## 🤖 AI Setup

Sentinel-OS supports **three AI providers** for snapshot commit message generation and code review:

| Provider | Cost | Setup |
|---|---|---|
| **Google Gemini** | Free tier available | Get key at [aistudio.google.com](https://aistudio.google.com) |
| **Ollama** (Local) | **Completely Free** | Install [Ollama](https://ollama.com), run `ollama run llama3` |
| **Nvidia NIM** | Free credits available | Get key at [build.nvidia.com](https://build.nvidia.com/explore/discover) |

Configure your chosen provider in the **Settings** tab of the app.

---

## 📋 Changelog

See [CHANGELOG.md](CHANGELOG.md) for the full list of changes.

---

## 🤝 Contributing

Contributions, issues, and feature requests are welcome! Feel free to open an issue or a pull request.
