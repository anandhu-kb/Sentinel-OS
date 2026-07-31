package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
	"sort"
	"os/exec"

	// Internal engine packages

	"sentinel-os/internal/ai"
	"sentinel-os/internal/logger"
	"sentinel-os/internal/sentinel"
	"sentinel-os/internal/snapshot"
	"sentinel-os/internal/watcher"

	"github.com/gen2brain/beeep"

	// modernc.org/sqlite is a pure-Go SQLite driver — zero CGo, zero external DLL.
	_ "modernc.org/sqlite"
)

// App is the central application struct. It is the single source of truth for
// shared application state (DB handle, runtime context, future engine refs).
//
// Every PUBLIC method on *App (capitalized name) is automatically exposed to
// the JavaScript frontend by Wails' binding system.
//
// Python analogy: App is a fusion of:
//   - Django's AppConfig   → lifecycle hooks (startup, shutdown)
//   - A DRF ViewSet        → its public methods are the "API endpoints" callable
//                            from JS instead of HTTP
//   - A dependency container → it holds and distributes shared resources (db, ctx)
type App struct {
	// ctx is the Wails runtime context. Saved during startup so we can call
	// Wails runtime functions (e.g., EventsEmit) from any method or goroutine.
	// Analogy: like Django's `request` object — it carries the live runtime state.
	ctx context.Context

	// db is the single shared SQLite connection pool for the entire application.
	// Using one *sql.DB (which is internally a connection pool) is the idiomatic
	// Go approach — it is safe for concurrent use by multiple goroutines.
	db *sql.DB

	// sentinelEngine is a pointer to The Sentinel uptime monitoring engine.
	// Storing a pointer means app.go and sentinel.go share the SAME object in
	// memory — mutations inside the engine (updating statusMap, etc.) are
	// visible through this reference. This is the Go equivalent of passing an
	// object by reference in Python.
	sentinelEngine *sentinel.Sentinel

	// watcherEngine is a pointer to The Watcher productivity telemetry engine.
	// It polls Windows APIs every 5 seconds to track foreground window focus.
	watcherEngine *watcher.Watcher

	// snapshotEngine is a pointer to The Snapshot Engine.
	// It uses a channel-based background worker to serialize snapshot operations.
	snapshotEngine *snapshot.Engine

}

// NewApp is the constructor for App. Called once in main() before wails.Run().
//
// It returns *App (a pointer to App) because:
//   - We will mutate its fields (ctx, db) after construction in startup().
//   - Without the pointer, Go would pass a COPY of the struct to Wails, and
//     mutations to that copy (setting a.ctx, a.db) would not be visible to
//     the methods Wails later calls on the original.
//
// Python analogy: `return &App{}` is like `return AppConfig()` — creating and
// returning an instance. The `&` operator takes the address of the new struct,
// giving us a pointer instead of a value.
func NewApp() *App {
	return &App{}
}

// startup is called by Wails immediately after the WebView window initializes.
// The `ctx` parameter is the live Wails runtime context — we store it for later use.
//
// This is our application bootstrap, equivalent to Django's AppConfig.ready()
// signal. It runs ONCE at the beginning of the application's life.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// ── Phase 0: Initialize Logger ─────────────────────────────────────────────
	if err := logger.Init("./logs"); err != nil {
		log.Printf("[startup] WARNING: Could not init file logger: %v", err)
	}
	logger.Info("=== Sentinel-OS Starting Up ===")

	// --- Database Initialization ---
	logger.Debug("Opening SQLite database: ./sentinel.db")
	db, err := sql.Open("sqlite", "./sentinel.db?_journal_mode=WAL")
	if err != nil {
		logger.Fatal("[startup] FATAL: Could not open database: %v", err)
	}
	a.db = db

	// Ping verifies the connection is actually reachable (triggers lazy connect).
	if err := a.db.Ping(); err != nil {
		logger.Fatal("[startup] FATAL: Database ping failed: %v", err)
	}
	logger.Info("Database connected and ping successful")

	// Run schema DDL — creates all tables if they don't already exist.
	if err := a.initSchema(); err != nil {
		logger.Fatal("[startup] FATAL: Schema initialization failed: %v", err)
	}
	logger.Info("Schema initialized successfully")

	logger.Info("Core systems online. Starting engines...")

	// ── Phase 1: The Sentinel ──────────────────────────────────────────────────
	logger.Debug("Starting Sentinel engine (uptime monitor)")
	a.sentinelEngine = sentinel.New(a.ctx, a.db)
	a.sentinelEngine.Start()
	logger.Info("Sentinel engine started")

	// ── Phase 2: Start The Watcher (Productivity Telemetry) ────────────────────
	logger.Debug("Starting Watcher engine (activity tracker)")
	a.watcherEngine = watcher.New(a.ctx, a.db)
	a.watcherEngine.Start()
	logger.Info("Watcher engine started")

	// ── Phase 3: Start The Snapshot Engine ────────────────────────────────
	logger.Debug("Starting Snapshot engine (version control)")
	a.snapshotEngine = snapshot.New(a.ctx, a.db)
	a.snapshotEngine.Start()
	logger.Info("Snapshot engine started")

	logger.Info("=== All systems online. Sentinel-OS is ready. ===")
}

// shutdown is called by Wails when the user closes the application window.
// Responsible for clean resource teardown — DB connections, goroutine signals, etc.
//
// Analogy: Django's teardown signal handlers / Python's context manager __exit__.
func (a *App) shutdown(ctx context.Context) {
	logger.Info("Shutdown signal received. Releasing resources...")
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			logger.Error("Error closing database: %v", err)
		} else {
			logger.Info("Database connection closed cleanly.")
		}
	}
	logger.Close()
}

// GetLogs returns the most recent log entries from the in-memory ring buffer.
// limit controls how many entries to fetch (max 500, the ring buffer size).
// This is safe to call from the frontend at any time.
func (a *App) GetLogs(limit int) []logger.LogEntry {
	return logger.GetRecentLogs(limit)
}

// initSchema executes DDL SQL to create all application tables idempotently.
// The `IF NOT EXISTS` clause makes this safe to run on every startup without
// destroying existing data.
//
// Analogy: this is your hand-written 0001_initial.py Django migration, expressed
// as raw SQL. We use raw SQL here (not an ORM) for maximum transparency and
// to keep the build dependency graph minimal.
func (a *App) initSchema() error {
	// A raw string literal in Go uses backticks (`...`).
	// Analogy: Python's triple-quoted strings ("""...""").
	schema := `
	-- ── The Sentinel: Uptime Monitor logs ─────────────────────────────────────
	CREATE TABLE IF NOT EXISTS uptime_logs (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		target      TEXT    NOT NULL,       -- "localhost:8000" or "https://myapp.com"
		status      TEXT    NOT NULL,       -- "UP" or "DOWN"
		latency_ms  INTEGER,               -- round-trip in milliseconds; NULL if unreachable
		checked_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS uptime_targets (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		name     TEXT    NOT NULL,
		address  TEXT    NOT NULL,
		kind     TEXT    NOT NULL
	);
	
	-- Seed default monitoring targets if the table is completely empty
	INSERT INTO uptime_targets (name, address, kind)
	SELECT 'Dev Server', 'localhost:8000', 'tcp'
	WHERE NOT EXISTS (SELECT 1 FROM uptime_targets);

	INSERT INTO uptime_targets (name, address, kind)
	SELECT 'Frontend Server', 'localhost:3000', 'tcp'
	WHERE NOT EXISTS (SELECT 1 FROM uptime_targets) AND NOT EXISTS (SELECT 1 FROM uptime_targets WHERE name = 'Frontend Server');

	INSERT INTO uptime_targets (name, address, kind)
	SELECT 'Google (Internet Check)', 'https://www.google.com', 'http'
	WHERE NOT EXISTS (SELECT 1 FROM uptime_targets) AND NOT EXISTS (SELECT 1 FROM uptime_targets WHERE name = 'Google (Internet Check)');

	-- ── The Watcher: Productivity Telemetry logs ────────────────────────────────
	CREATE TABLE IF NOT EXISTS productivity_logs (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		app_name     TEXT    NOT NULL,      -- process name, e.g. "Code.exe"
		window_title TEXT    NOT NULL,      -- full window title string
		duration_s   INTEGER NOT NULL,      -- seconds spent in this window focus session
		logged_at    DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS watcher_rules (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		match_string TEXT    NOT NULL,
		name         TEXT    NOT NULL,
		category     TEXT    NOT NULL,
		is_regex     BOOLEAN NOT NULL DEFAULT 0
	);

	-- Seed default watcher rules if the table is completely empty
	INSERT INTO watcher_rules (match_string, name, category, is_regex)
	SELECT 'code', 'VS Code', 'IDE / Code', 0
	WHERE NOT EXISTS (SELECT 1 FROM watcher_rules);

	INSERT INTO watcher_rules (match_string, name, category, is_regex)
	SELECT 'idea', 'IntelliJ', 'IDE / Code', 0
	WHERE NOT EXISTS (SELECT 1 FROM watcher_rules WHERE name = 'IntelliJ');

	INSERT INTO watcher_rules (match_string, name, category, is_regex)
	SELECT 'chrome', 'Chrome', 'Browser / Docs', 0
	WHERE NOT EXISTS (SELECT 1 FROM watcher_rules WHERE name = 'Chrome');

	INSERT INTO watcher_rules (match_string, name, category, is_regex)
	SELECT 'slack', 'Slack', 'Communication', 0
	WHERE NOT EXISTS (SELECT 1 FROM watcher_rules WHERE name = 'Slack');

	INSERT INTO watcher_rules (match_string, name, category, is_regex)
	SELECT 'discord', 'Discord', 'Communication', 0
	WHERE NOT EXISTS (SELECT 1 FROM watcher_rules WHERE name = 'Discord');

	INSERT INTO watcher_rules (match_string, name, category, is_regex)
	SELECT 'powershell', 'PowerShell', 'Terminal', 0
	WHERE NOT EXISTS (SELECT 1 FROM watcher_rules WHERE name = 'PowerShell');

	-- ── The Snapshot Engine: version history metadata ────────────────────
	CREATE TABLE IF NOT EXISTS snapshots (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		project_path TEXT    NOT NULL,      -- absolute path to the watched project root
		commit_hash  TEXT    NOT NULL,      -- SHA-1 hash uniquely identifying this snapshot
		message      TEXT,                  -- optional user-provided commit message
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	-- Content-addressable blob store: each unique file version stored exactly ONCE.
	-- INSERT OR IGNORE deduplicates: if file X is unchanged across 10 snapshots,
	-- its content occupies one row here, referenced 10 times in snapshot_files.
	CREATE TABLE IF NOT EXISTS snapshot_blobs (
		hash    TEXT PRIMARY KEY,   -- SHA-1 hex of file content
		content BLOB NOT NULL       -- raw file bytes
	);

	-- Tree index: maps each snapshot to the file blobs that make up its state.
	CREATE TABLE IF NOT EXISTS snapshot_files (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		snapshot_id INTEGER NOT NULL REFERENCES snapshots(id),
		rel_path    TEXT    NOT NULL,   -- forward-slash normalized path within project
		file_hash   TEXT    NOT NULL,   -- SHA-1 → references snapshot_blobs.hash
		size_bytes  INTEGER NOT NULL,
		line_count  INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_snapshot_files_sid ON snapshot_files(snapshot_id);



	-- ── Pomodoro Timer ──────────────────────────────────────────────────────────────────────────────
	CREATE TABLE IF NOT EXISTS pomodoro_sessions (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		start_time       DATETIME DEFAULT CURRENT_TIMESTAMP,
		end_time         DATETIME,
		duration_minutes INTEGER NOT NULL,
		status           TEXT NOT NULL,
		task_name        TEXT
	);
	`

	_, err := a.db.Exec(schema)
	if err != nil {
		return err
	}


	a.db.Exec(`ALTER TABLE pomodoro_sessions ADD COLUMN task_name TEXT`)
	
	return nil
}

// ── JS-Bound Methods (The Sentinel API) ───────────────────────────────────────
//
// These methods are PUBLIC (capitalized) on *App, so Wails exposes them to JS.
// In JavaScript they are called as async functions returning Promises:
//
//   import { AddMonitorTarget, GetMonitorStatuses, GetUptimeLogs }
//     from './wailsjs/go/main/App'
//
//   await AddMonitorTarget("localhost:5432", "tcp")
//   const statuses = await GetMonitorStatuses()
//   const logs     = await GetUptimeLogs(50)

// UptimeLogRow is the Go struct that maps to one row in the uptime_logs table.
// It is returned by GetUptimeLogs and auto-serialized to a JS object by Wails.
// Struct tags (`json:"..."`) control the JSON field names seen in JavaScript.
type UptimeLogRow struct {
	ID        int64  `json:"id"`
	Target    string `json:"target"`
	Status    string `json:"status"`    // "UP" or "DOWN"
	LatencyMs int64  `json:"latencyMs"` // milliseconds
	CheckedAt string `json:"checkedAt"` // RFC3339 timestamp string
}

// AddMonitorTarget adds a new endpoint to the database and reloads the engine.
func (a *App) AddMonitorTarget(name, address, kind string) string {
	switch sentinel.TargetKind(kind) {
	case sentinel.KindTCP, sentinel.KindHTTP:
		_, err := a.db.Exec(`INSERT INTO uptime_targets (name, address, kind) VALUES (?, ?, ?)`, name, address, kind)
		if err != nil {
			return err.Error()
		}
		a.sentinelEngine.ReloadTargets()
		return ""
	default:
		return fmt.Sprintf("invalid kind %q: must be \"tcp\" or \"http\"", kind)
	}
}

// EditMonitorTarget updates an existing target in the DB and reloads the engine.
func (a *App) EditMonitorTarget(id int64, name, address, kind string) string {
	switch sentinel.TargetKind(kind) {
	case sentinel.KindTCP, sentinel.KindHTTP:
		_, err := a.db.Exec(`UPDATE uptime_targets SET name = ?, address = ?, kind = ? WHERE id = ?`, name, address, kind, id)
		if err != nil {
			return err.Error()
		}
		a.sentinelEngine.ReloadTargets()
		return ""
	default:
		return fmt.Sprintf("invalid kind %q: must be \"tcp\" or \"http\"", kind)
	}
}

// RemoveMonitorTarget deletes a target from the DB and reloads the engine.
func (a *App) RemoveMonitorTarget(id int64) error {
	_, err := a.db.Exec(`DELETE FROM uptime_targets WHERE id = ?`, id)
	if err == nil {
		a.sentinelEngine.ReloadTargets()
	}
	return err
}

// GetMonitorStatuses returns the current UP/DOWN status of all monitored targets.
// Called from JavaScript as: const statuses = await window.go.main.App.GetMonitorStatuses()
// Returns: []sentinel.StatusSnapshot → JS Array of { address, kind, isUp, latencyMs, checkedAt }
func (a *App) GetMonitorStatuses() []sentinel.StatusSnapshot {
	return a.sentinelEngine.GetCurrentStatuses()
}

// GetUptimeLogs fetches the most recent `limit` rows from uptime_logs for a specific target, newest first.
// Called from JavaScript as: const logs = await window.go.main.App.GetUptimeLogs("localhost:8000", 100)
// Returns: []UptimeLogRow → JS Array of log objects
func (a *App) GetUptimeLogs(target string, limit int) ([]UptimeLogRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100 // safe default
	}

	rows, err := a.db.QueryContext(a.ctx,
		`SELECT id, target, status, COALESCE(latency_ms, 0), checked_at
		 FROM uptime_logs
		 WHERE target = ?
		 ORDER BY id DESC
		 LIMIT ?`,
		target, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []UptimeLogRow
	for rows.Next() {
		var row UptimeLogRow
		if err := rows.Scan(&row.ID, &row.Target, &row.Status, &row.LatencyMs, &row.CheckedAt); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	// rows.Err() catches any error that occurred during iteration.
	// Analogy: checking for exceptions after a Django queryset loop.
	return results, rows.Err()
}

// GetUptimeStats calculates the overall "UP" percentage for all targets.
// Returns a map of target address -> up percentage (0-100).
func (a *App) GetUptimeStats() (map[string]float64, error) {
	rows, err := a.db.QueryContext(a.ctx, `
		SELECT 
			target,
			COUNT(CASE WHEN status = 'UP' THEN 1 END) * 100.0 / COUNT(*) as up_pct
		FROM uptime_logs
		GROUP BY target
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make(map[string]float64)
	for rows.Next() {
		var target string
		var upPct float64
		if err := rows.Scan(&target, &upPct); err != nil {
			return nil, err
		}
		stats[target] = upPct
	}
	return stats, rows.Err()
}

// ── JS-Bound Methods (The Watcher API) ────────────────────────────────────────

// GetUniqueApps returns a list of all application names we've recorded in productivity_logs.
func (a *App) GetUniqueApps() ([]string, error) {
	return a.watcherEngine.GetUniqueApps()
}

// GetProductivityHistoryByApp queries SQLite for productivity data for a specific app over the past N days.
func (a *App) GetProductivityHistoryByApp(appName string, days int) ([]watcher.AppHistoryEntry, error) {
	return a.watcherEngine.GetProductivityHistoryByApp(appName, days)
}

// GetUnmappedPrograms returns a list of executables from productivity_logs that don't match any existing rules.
func (a *App) GetUnmappedPrograms() ([]string, error) {
	return a.watcherEngine.GetUnmappedPrograms()
}

// GetDetailedLogs queries SQLite for all raw session logs over the past N days.
func (a *App) GetDetailedLogs(days int) ([]watcher.DetailedLog, error) {
	return a.watcherEngine.GetDetailedLogs(days)
}

// GetActiveWindow returns the currently tracked foreground window info.
// Called from JS as: const info = await window.go.main.App.GetActiveWindow()
// Returns: { title, exeName, category, startedAt } or a zero-value if not yet tracked.
func (a *App) GetActiveWindow() watcher.WindowInfo {
	return a.watcherEngine.GetCurrentWindow()
}

// GetTodayProductivityStats returns today's focus time aggregated by category.
// Called from JS as: const stats = await window.go.main.App.GetTodayProductivityStats()
// Returns: []{ category, totalSecs, apps: [{ exeName, totalSecs }] }
//
// Note: the currently ACTIVE window session is not included — it is still being
// timed and hasn't been flushed to the DB. Add GetActiveWindow() data for a
// real-time view of the in-progress session.
func (a *App) GetTodayProductivityStats() ([]watcher.CategoryStat, error) {
	return a.watcherEngine.GetTodayStats()
}

// GetProductivityHistory returns the productivity history over the last N days.
// Called from JS as: const history = await window.go.main.App.GetProductivityHistory(7)
func (a *App) GetProductivityHistory(days int) ([]watcher.HistoryEntry, error) {
	return a.watcherEngine.GetProductivityHistory(days)
}

// AddWatcherRule adds a new classification rule to the database and reloads rules.
func (a *App) AddWatcherRule(matchString, name, category string, isRegex bool) error {
	logger.Info("AddWatcherRule: match=%q name=%q category=%q", matchString, name, category)
	_, err := a.db.Exec(`INSERT INTO watcher_rules (match_string, name, category, is_regex) VALUES (?, ?, ?, ?)`, matchString, name, category, isRegex)
	if err == nil {
		a.watcherEngine.ReloadRules()
	}
	return err
}

// EditWatcherRule updates an existing classification rule and reloads rules.
func (a *App) EditWatcherRule(id int64, matchString, name, category string, isRegex bool) error {
	_, err := a.db.Exec(`UPDATE watcher_rules SET match_string = ?, name = ?, category = ?, is_regex = ? WHERE id = ?`, matchString, name, category, isRegex, id)
	if err == nil {
		a.watcherEngine.ReloadRules()
	}
	return err
}

// RemoveWatcherRule deletes a classification rule and reloads rules.
func (a *App) RemoveWatcherRule(id int64) error {
	logger.Info("RemoveWatcherRule: id=%d", id)
	_, err := a.db.Exec(`DELETE FROM watcher_rules WHERE id = ?`, id)
	if err == nil {
		a.watcherEngine.ReloadRules()
	}
	return err
}

// GetWatcherRules returns all currently defined watcher rules.
func (a *App) GetWatcherRules() ([]watcher.Rule, error) {
	rows, err := a.db.Query(`SELECT id, match_string, name, category, is_regex FROM watcher_rules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []watcher.Rule
	for rows.Next() {
		var r watcher.Rule
		if err := rows.Scan(&r.ID, &r.MatchString, &r.Name, &r.Category, &r.IsRegex); err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// ── JS-Bound Methods (The Snapshot API) ───────────────────────────────────────
//
// Called from JS as:
//   import { CreateSnapshot, GetSnapshotHistory, GetSnapshotDiff } from './wailsjs/go/main/App'
//
//   const snap = await CreateSnapshot("E:/myproject", "Before refactor")
//   const history = await GetSnapshotHistory("E:/myproject", 20)
//   const diff = await GetSnapshotDiff(1, 2)

// CreateSnapshot takes a deterministic snapshot of the project at projectPath.
// Runs in the Snapshot Engine's background worker goroutine (non-blocking to UI).
// Returns the new SnapshotInfo on success, or an error string if it fails.
//
//	projectPath — absolute path to the project root directory to snapshot
//	message     — optional human-readable label (like a commit message)
func (a *App) CreateSnapshot(projectPath, message string) (snapshot.SnapshotInfo, error) {
	logger.Info("CreateSnapshot: path=%q message=%q", projectPath, message)
	snap, err := a.snapshotEngine.TakeSnapshot(projectPath, message)
	if err != nil {
		logger.Error("CreateSnapshot FAILED: %v", err)
		return snap, err
	}
	logger.Info("CreateSnapshot OK: id=%d hash=%s files=%d size=%d bytes",
		snap.ID, snap.CommitHash, snap.FileCount, snap.TotalBytes)
	return snap, nil
}

// GetSnapshotHistory returns the N most recent snapshots for a project, newest first.
// Each entry has: { id, projectPath, commitHash, fullHash, message, fileCount, totalBytes, createdAt }
func (a *App) GetSnapshotHistory(projectPath string, limit int) ([]snapshot.SnapshotInfo, error) {
	logger.Debug("GetSnapshotHistory: path=%q limit=%d", projectPath, limit)
	return a.snapshotEngine.GetHistory(projectPath, limit)
}

// GetSnapshotDiff computes the file-level and line-level differences between two snapshots.
// oldID and newID are snapshot IDs from GetSnapshotHistory.
// Returns: { addedFiles, deletedFiles, modifiedFiles: [{relPath, linesAdded, linesDeleted}], summary }
func (a *App) GetSnapshotDiff(oldID, newID int64) (snapshot.DiffResult, error) {
	logger.Debug("GetSnapshotDiff: oldID=%d newID=%d", oldID, newID)
	diff, err := a.snapshotEngine.GetDiff(oldID, newID)
	if err != nil {
		logger.Error("GetSnapshotDiff FAILED: %v", err)
	}
	return diff, err
}

// AutoGenerateCommitMessage uses AI to generate a commit message for a snapshot based on its diff.
func (a *App) AutoGenerateCommitMessage(prevID, currentID int64, aiProvider, aiConfig string) (string, error) {
	logger.Info("AutoGenerateCommitMessage: prevID=%d currentID=%d provider=%s", prevID, currentID, aiProvider)
	if prevID == 0 {
		msg := "Initial commit"
		err := a.snapshotEngine.UpdateSnapshot(currentID, msg)
		return msg, err
	}

	diff, err := a.snapshotEngine.GetDiff(prevID, currentID)
	if err != nil {
		return "", err
	}

	var diffText string
	for _, f := range diff.ModifiedFiles {
		diffText += fmt.Sprintf("--- a/%s\n+++ b/%s\n", f.RelPath, f.RelPath)
		for _, c := range f.Chunks {
			lines := strings.Split(c.Text, "\n")
			for _, line := range lines {
				if line == "" {
					continue
				}
				if c.Type == -1 {
					diffText += "-" + line + "\n"
				} else if c.Type == 1 {
					diffText += "+" + line + "\n"
				} else {
					diffText += " " + line + "\n"
				}
			}
		}
	}
	for _, f := range diff.AddedFiles {
		diffText += fmt.Sprintf("Added: %s\n", f)
	}
	for _, f := range diff.DeletedFiles {
		diffText += fmt.Sprintf("Deleted: %s\n", f)
	}

	var msg string
	if aiProvider == "ollama" {
		msg, err = ai.GenerateCommitMessageOllama(diffText, aiConfig)
	} else if aiProvider == "nvidia" {
		msg, err = ai.GenerateCommitMessageNvidia(diffText, aiConfig)
	} else {
		msg, err = ai.GenerateCommitMessage(diffText, aiConfig)
	}
	if err != nil {
		logger.Error("AutoGenerateCommitMessage FAILED: %v", err)
		return "", err
	}
	logger.Info("AutoGenerateCommitMessage OK: msg=%q", msg)

	err = a.snapshotEngine.UpdateSnapshot(currentID, msg)
	return msg, err
}

// AutoGenerateAIReview uses AI to review a snapshot diff and generate an HTML summary.
func (a *App) AutoGenerateAIReview(prevID, currentID int64, aiProvider, aiConfig string) (string, error) {
	logger.Info("AutoGenerateAIReview: prevID=%d currentID=%d provider=%s", prevID, currentID, aiProvider)
	if prevID == 0 {
		logger.Debug("AutoGenerateAIReview: initial snapshot, no diff")
		return "<b>Initial commit</b>. No diff to analyze.", nil
	}

	diff, err := a.snapshotEngine.GetDiff(prevID, currentID)
	if err != nil {
		return "", err
	}

	var diffText string
	for _, f := range diff.ModifiedFiles {
		diffText += fmt.Sprintf("--- a/%s\n+++ b/%s\n", f.RelPath, f.RelPath)
		for _, c := range f.Chunks {
			lines := strings.Split(c.Text, "\n")
			for _, line := range lines {
				if line == "" {
					continue
				}
				if c.Type == -1 {
					diffText += "-" + line + "\n"
				} else if c.Type == 1 {
					diffText += "+" + line + "\n"
				} else {
					diffText += " " + line + "\n"
				}
			}
		}
	}
	for _, f := range diff.AddedFiles {
		diffText += fmt.Sprintf("Added: %s\n", f)
	}
	for _, f := range diff.DeletedFiles {
		diffText += fmt.Sprintf("Deleted: %s\n", f)
	}

	if aiProvider == "ollama" {
		return ai.GenerateAIReviewOllama(diffText, aiConfig)
	} else if aiProvider == "nvidia" {
		return ai.GenerateAIReviewNvidia(diffText, aiConfig)
	}
	result, err := ai.GenerateAIReview(diffText, aiConfig)
	if err != nil {
		logger.Error("AutoGenerateAIReview FAILED: %v", err)
	} else {
		logger.Info("AutoGenerateAIReview OK: responseLen=%d chars", len(result))
	}
	return result, err
}

// UpdateSnapshot updates the message of an existing snapshot.
func (a *App) UpdateSnapshot(id int64, message string) error {
	logger.Debug("UpdateSnapshot: id=%d message=%q", id, message)
	return a.snapshotEngine.UpdateSnapshot(id, message)
}

// DeleteSnapshot deletes an existing snapshot.
func (a *App) DeleteSnapshot(id int64) error {
	logger.Info("DeleteSnapshot: id=%d", id)
	err := a.snapshotEngine.DeleteSnapshot(id)
	if err != nil {
		logger.Error("DeleteSnapshot FAILED: %v", err)
	}
	return err
}

// RestoreSnapshot restores the project directory to the state of the snapshot.
func (a *App) RestoreSnapshot(id int64, projectPath string) error {
	logger.Info("RestoreSnapshot: id=%d path=%q", id, projectPath)
	err := a.snapshotEngine.RestoreSnapshot(id, projectPath)
	if err != nil {
		logger.Error("RestoreSnapshot FAILED: %v", err)
	} else {
		logger.Info("RestoreSnapshot OK")
	}
	return err
}


// ── System Resource Monitoring ──

type TopProcess struct {
	Name   string  `json:"name"`
	CPU    float64 `json:"cpu"`
	Memory float32 `json:"memory"`
}

type ResourceStats struct {
	CPUPercent   float64      `json:"cpuPercent"`
	RAMPercent   float64      `json:"ramPercent"`
	DiskPercent  float64      `json:"diskPercent"`
	NetUpload    uint64       `json:"netUpload"`
	NetDownload  uint64       `json:"netDownload"`
	TopProcesses []TopProcess `json:"topProcesses"`
	HostOS       string       `json:"hostOs"`
	HostUptime   uint64       `json:"hostUptime"`
	HostBootTime uint64       `json:"hostBootTime"`
}

// GetSystemResources retrieves the current CPU, RAM, Disk, Net, Top Processes, and Host info.
func (a *App) GetSystemResources() (ResourceStats, error) {
	var stats ResourceStats

	// CPU
	c, err := cpu.Percent(0, false)
	if err == nil && len(c) > 0 {
		stats.CPUPercent = c[0]
	}

	// RAM
	v, err := mem.VirtualMemory()
	if err == nil {
		stats.RAMPercent = v.UsedPercent
	}

	// Host
	h, err := host.Info()
	if err == nil {
		stats.HostOS = h.Platform + " " + h.PlatformVersion
		stats.HostUptime = h.Uptime
		stats.HostBootTime = h.BootTime
	}

	// Disk
	diskPath := "C:\\"
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		diskPath = "/"
	}
	d, err := disk.Usage(diskPath)
	if err == nil {
		stats.DiskPercent = d.UsedPercent
	}
	
	// Network
	netStats, err := net.IOCounters(false)
	if err == nil && len(netStats) > 0 {
		stats.NetUpload = netStats[0].BytesSent
		stats.NetDownload = netStats[0].BytesRecv
	}
	
	// Top Processes
	procs, err := process.Processes()
	if err == nil {
		var procStats []TopProcess
		for _, p := range procs {
			name, _ := p.Name()
			cpuP, _ := p.CPUPercent()
			memP, _ := p.MemoryPercent()
			if cpuP > 0.1 || memP > 0.1 {
				procStats = append(procStats, TopProcess{Name: name, CPU: cpuP, Memory: memP})
			}
		}
		sort.Slice(procStats, func(i, j int) bool {
			return procStats[i].CPU > procStats[j].CPU
		})
		limit := 5
		if len(procStats) < limit {
			limit = len(procStats)
		}
		stats.TopProcesses = procStats[:limit]
	}

	return stats, nil
}

// ── Pomodoro Timer ────────────────────────────────────────────────────────────

type PomodoroSession struct {
	ID              int64   `json:"id"`
	StartTime       string  `json:"startTime"`
	EndTime         *string `json:"endTime"`
	DurationMinutes int     `json:"durationMinutes"`
	Status          string  `json:"status"`
	TaskName        string  `json:"taskName"`
}

// StartPomodoro starts a new session and returns its ID
func (a *App) StartPomodoro(durationMinutes int, taskName string) (int64, error) {
	logger.Info("StartPomodoro: duration=%d task=%q", durationMinutes, taskName)
	res, err := a.db.Exec(`INSERT INTO pomodoro_sessions (duration_minutes, status, task_name) VALUES (?, 'running', ?)`, durationMinutes, taskName)
	if err != nil {
		logger.Error("StartPomodoro FAILED: %v", err)
		return 0, err
	}
	id, _ := res.LastInsertId()
	logger.Debug("StartPomodoro OK: sessionID=%d", id)
	return id, nil
}

// CompletePomodoro marks a session as completed
func (a *App) CompletePomodoro(id int64) error {
	logger.Info("CompletePomodoro: id=%d", id)
	_, err := a.db.Exec(`UPDATE pomodoro_sessions SET status = 'completed', end_time = CURRENT_TIMESTAMP WHERE id = ?`, id)
	if err != nil {
		logger.Error("CompletePomodoro FAILED: %v", err)
	}
	return err
}

// CancelPomodoro marks a session as cancelled
func (a *App) CancelPomodoro(id int64) error {
	logger.Info("CancelPomodoro: id=%d", id)
	_, err := a.db.Exec(`UPDATE pomodoro_sessions SET status = 'cancelled', end_time = CURRENT_TIMESTAMP WHERE id = ?`, id)
	if err != nil {
		logger.Error("CancelPomodoro FAILED: %v", err)
	}
	return err
}

// TriggerDistractionAlert fires a native OS notification.
func (a *App) TriggerDistractionAlert() error {
	logger.Warn("TriggerDistractionAlert: distraction detected during focus session")
	return beeep.Alert("Sentinel-OS", "Distraction detected! Get back to work!", "")
}

// GetPomodoroHistory retrieves the last 50 pomodoro sessions
func (a *App) GetPomodoroHistory() ([]PomodoroSession, error) {
	rows, err := a.db.Query(`SELECT id, start_time, end_time, duration_minutes, status, COALESCE(task_name,'') FROM pomodoro_sessions ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []PomodoroSession
	for rows.Next() {
		var s PomodoroSession
		var endTime sql.NullString
		err := rows.Scan(&s.ID, &s.StartTime, &endTime, &s.DurationMinutes, &s.Status, &s.TaskName)
		if err != nil {
			return nil, err
		}
		if endTime.Valid {
			val := endTime.String
			s.EndTime = &val
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// GetRunningApps uses the Windows tasklist command to get a list of currently running unique .exe names.
func (a *App) GetRunningApps() ([]string, error) {
	cmd := exec.Command("tasklist", "/FO", "CSV", "/NH")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	
	appMap := make(map[string]bool)
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// tasklist CSV format: "Image Name","PID","Session Name","Session#","Mem Usage"
		parts := strings.SplitN(line, ",", 2)
		if len(parts) > 0 {
			appName := strings.Trim(parts[0], `"`)
			if strings.HasSuffix(strings.ToLower(appName), ".exe") {
				appMap[appName] = true
			}
		}
	}
	
	var apps []string
	for app := range appMap {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	return apps, nil
}
