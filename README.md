# 🛡️ Sentinel-OS

**Sentinel-OS** is a powerful, locally-hosted productivity and system monitoring dashboard built with [Wails](https://wails.io/) (Go backend + Vanilla Web frontend). It serves as your personal command center, designed to help you stay focused, secure your data, and keep a watchful eye on your system and services.

---

## ✨ Features

- 🎯 **Pomodoro Focus Timer:** A built-in, highly visual Pomodoro timer to manage focus sessions. Tracks your tasks, logs session history, and automatically cycles between focus and break modes with audio cues.
- 👁️ **Watcher (Productivity Telemetry):** Automatically tracks your active windows and maps them into categories (Productive, Unproductive, Neutral) so you can review where your time is going.
- 📊 **System Resources:** Real-time polling of your machine's CPU, Memory, and Disk usage via a sleek, dynamic UI.
- 📡 **Sentinel (Uptime Monitoring):** Monitor the uptime, latency, and status of your favorite web services and APIs. 
- 🛡️ **Guardian (Automated Backups):** Schedule encrypted, automated backups for your most important project directories.
- 📸 **Snapshots:** Integrated window capture and screen clipping capabilities.
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
   - [Wails v2 CLI](https://wails.io/docs/gettingstarted/installation)

2. **Clone the repository:**
   ```bash
   git clone https://github.com/anandhu-kb/Sentinel-OS.git
   cd Sentinel-OS
   ```

3. **Build the app:**
   ```bash
   wails build
   ```
   *The compiled standalone executable will be placed in the `build/bin/` directory.*

---

## 🔒 Privacy & Security

Sentinel-OS is designed with privacy in mind. 
- **Local-First Database:** All of your telemetry, history, and configuration are stored locally in an SQLite database (`sentinel.db`). 
- **Encryption:** Backup archives managed by Guardian can be encrypted using a local secret key (`sentinel.key`).
- **No Telemetry:** We don't phone home. Your productivity data stays on your machine.

---

## 🛠️ Tech Stack
- **Backend:** Go, SQLite3 (go-sqlite3)
- **Frontend:** HTML5, Vanilla JavaScript, CSS3
- **Framework:** Wails v2
