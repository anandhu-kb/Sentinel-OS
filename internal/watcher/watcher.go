//go:build windows

// The //go:build directive above is a compile-time constraint — this entire file
// is only compiled when the target OS is Windows. The Go toolchain silently
// skips it on Linux/macOS, preventing compilation errors from Windows-only imports.
//
// Python analogy: like `import sys; assert sys.platform == 'win32'` at the top,
// but enforced by the compiler rather than raising a runtime error.

// Package watcher implements The Watcher — Sentinel-OS's productivity telemetry engine.
//
// Architecture overview:
//
//	startup() in app.go
//	    └─ watcher.New(ctx, db) → constructs engine
//	    └─ w.Start()            → launches ONE background goroutine (w.run)
//	           │
//	           ├─ every 5s tick → w.poll()
//	           │       ├─ calls Windows API: GetForegroundWindow → GetWindowTextW
//	           │       │                     GetWindowThreadProcessId → OpenProcess
//	           │       │                     QueryFullProcessImageNameW
//	           │       ├─ classifies exe into a productivity category
//	           │       ├─ if window CHANGED: flush old session to SQLite
//	           │       └─ emit "watcher:window_changed" event to JS frontend
//	           │
//	           └─ ctx.Done() → flush current session → goroutine exits cleanly
package watcher

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ── Windows API Setup ──────────────────────────────────────────────────────────
//
// Go's syscall.NewLazyDLL loads a Windows DLL and gives us a handle to call its
// exported C functions from Go. This is the direct equivalent of Python's ctypes:
//
//   Python ctypes:                        Go syscall:
//   ──────────────────────────────────    ──────────────────────────────────────────
//   ctypes.windll.user32               ≈  syscall.NewLazyDLL("user32.dll")
//   ctypes.windll.user32.GetFore…      ≈  user32.NewProc("GetForegroundWindow")
//   proc(arg1, arg2)                   ≈  proc.Call(arg1, arg2)
//   ctypes.create_unicode_buffer(512)  ≈  make([]uint16, 512)
//   ctypes.byref(buf) / ctypes.cast()  ≈  uintptr(unsafe.Pointer(&buf[0]))
//
// "Lazy" = the DLL is not physically loaded until the first .Call() — identical
// to Python's ctypes lazy loading behavior. No startup overhead.
var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	// Each NewProc is a handle to one exported function within the DLL.
	// None of these cause actual loading until .Call() is invoked.
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
)

// Windows access-rights constant for OpenProcess.
// PROCESS_QUERY_LIMITED_INFORMATION (0x1000) gives us just enough permission
// to query the executable path — no elevation required for most user processes.
const processQueryLimitedInformation = 0x1000

// ── Classification Rules ───────────────────────────────────────────────────────

type Rule struct {
	ID          int64
	MatchString string
	Name        string
	Category    string
	IsRegex     bool
}

// loadRulesFromDB reads all user-defined productivity categorization rules.
func (w *Watcher) loadRulesFromDB() {
	w.mu.Lock()
	defer w.mu.Unlock()

	rows, err := w.db.Query(`SELECT id, match_string, name, category, is_regex FROM watcher_rules`)
	if err != nil {
		log.Printf("[Watcher] Error loading rules: %v", err)
		return
	}
	defer rows.Close()

	var newRules []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.MatchString, &r.Name, &r.Category, &r.IsRegex); err == nil {
			newRules = append(newRules, r)
		}
	}
	w.rules = newRules
}

// ReloadRules can be called from app.go to refresh the rule cache.
func (w *Watcher) ReloadRules() {
	w.loadRulesFromDB()
}

// classifyByExe returns the mapped name and category for a given executable or window title.
func (w *Watcher) classifyByExe(exeName string, windowTitle string) (string, string) {
	exeLower := strings.ToLower(exeName)
	titleLower := strings.ToLower(windowTitle)

	w.mu.RLock()
	defer w.mu.RUnlock()

	for _, rule := range w.rules {
		if rule.IsRegex {
			// (Optional) Implement regex matching if needed.
		} else {
			if strings.Contains(exeLower, strings.ToLower(rule.MatchString)) || strings.Contains(titleLower, strings.ToLower(rule.MatchString)) {
				// Return the rule's Name (if provided, else use raw exeName) and the rule's Category
				mappedName := rule.Name
				if mappedName == "" {
					mappedName = exeName
				}
				return mappedName, rule.Category
			}
		}
	}
	return exeName, "Other"
}

// ── Exported Types (JS-facing) ─────────────────────────────────────────────────

// AppHistoryEntry represents time spent on a specific app on a specific date.
type AppHistoryEntry struct {
	Date      string `json:"date"`
	TotalSecs int64  `json:"totalSecs"`
}

// DetailedLog represents a raw session entry.
type DetailedLog struct {
	AppName      string `json:"appName"`
	Category     string `json:"category"`
	Date         string `json:"date"`
	StartedAt    string `json:"startedAt"`
	EndedAt      string `json:"endedAt"`
	DurationSecs int64  `json:"durationSecs"`
}

// WindowInfo is the JSON-serializable snapshot of the currently focused window.
// It is returned by GetCurrentWindow() and emitted via "watcher:window_changed".
// Wails auto-serializes this to a JS object: { title, exeName, category, startedAt }
type WindowInfo struct {
	Title     string `json:"title"`     // full window title text
	ExeName   string `json:"exeName"`   // process basename, e.g. "Code.exe"
	Category  string `json:"category"`  // classified productivity category
	StartedAt string `json:"startedAt"` // RFC3339 timestamp when this focus began
}

// CategoryStat aggregates total focused time for one productivity category.
// Returned by GetTodayStats() and auto-serialized to JS.
type CategoryStat struct {
	Category  string    `json:"category"`
	TotalSecs int64     `json:"totalSecs"` // total focus time in seconds today
	Apps      []AppStat `json:"apps"`      // per-app breakdown within this category
}

// AppStat is one application entry within a CategoryStat.
type AppStat struct {
	ExeName   string `json:"exeName"`
	TotalSecs int64  `json:"totalSecs"`
}

// HistoryEntry aggregates total focused time per day.
type HistoryEntry struct {
	Date  string         `json:"date"`  // YYYY-MM-DD
	Stats []CategoryStat `json:"stats"` // Categories for that day
}

// ── Internal State ─────────────────────────────────────────────────────────────

// activeSession tracks the currently focused window and when it started.
// This is internal state — never directly exposed to JavaScript.
type activeSession struct {
	title     string    // window title at the time tracking started
	exeName   string    // process basename, e.g. "Code.exe"
	category  string    // pre-classified category
	startedAt time.Time // when this application came into focus
}

// ── Watcher Engine ─────────────────────────────────────────────────────────────

// Watcher is the productivity telemetry engine.
//
// Concurrency model:
//   - ONE goroutine (run) polls Windows APIs every 5 seconds.
//   - The mu RWMutex protects the `current` session field.
//   - JS-callable methods (GetCurrentWindow) hold RLock for reads.
//   - poll() holds WLock only during the brief in-memory state update.
//     DB writes and event emissions happen OUTSIDE the lock.
type Watcher struct {
	ctx context.Context
	db  *sql.DB

	mu      sync.RWMutex  // guards `current` and `rules`
	current *activeSession // nil until first poll; updated on every window change
	rules   []Rule         // cached categorization rules
}

// New constructs the Watcher engine. Call Start() to begin polling.
func New(ctx context.Context, db *sql.DB) *Watcher {
	w := &Watcher{
		ctx: ctx,
		db:  db,
	}
	w.loadRulesFromDB()
	return w
}

// ── Public API ─────────────────────────────────────────────────────────────────

// Start launches the monitoring loop in a background goroutine and returns immediately.
//
// Go concurrency note: `go w.run()` is non-blocking. It schedules w.run()
// on the Go runtime's goroutine scheduler and returns instantly.
// Python equivalent: threading.Thread(target=w.run, daemon=True).start()
func (w *Watcher) Start() {
	go w.run()
	log.Println("[Watcher] Productivity telemetry engine started (polling every 5s).")
}

// GetCurrentWindow returns a snapshot of the currently tracked window.
// Called from JS via: const info = await window.go.main.App.GetActiveWindow()
// Returns a zero-value WindowInfo{Category: "Other"} if no window is tracked yet.
func (w *Watcher) GetCurrentWindow() WindowInfo {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if w.current == nil {
		return WindowInfo{Category: "Other"}
	}
	return WindowInfo{
		Title:     w.current.title,
		ExeName:   w.current.exeName,
		Category:  w.current.category,
		StartedAt: w.current.startedAt.Format(time.RFC3339),
	}
}

// GetTodayStats queries SQLite for today's productivity data, aggregated by category.
// Called from JS via: const stats = await window.go.main.App.GetTodayProductivityStats()
//
// Returns []CategoryStat sorted by total focus time (descending), each containing
// a per-app breakdown. The current IN-PROGRESS session is NOT included (it hasn't
// been flushed to the DB yet — it's still being timed).
func (w *Watcher) GetTodayStats() ([]CategoryStat, error) {
	// Query today's completed sessions, grouped by application name.
	// date('now', 'localtime') returns today's date in the machine's local timezone,
	// ensuring we aggregate "today" correctly regardless of UTC offset.
	rows, err := w.db.QueryContext(w.ctx, `
		SELECT app_name, SUM(duration_s) AS total_secs
		FROM   productivity_logs
		WHERE  date(logged_at, 'localtime') = date('now', 'localtime')
		GROUP  BY app_name
		ORDER  BY total_secs DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// catMap aggregates per-app rows into per-category totals.
	// Python analogy: defaultdict(lambda: CategoryStat(apps=[], total=0))
	catMap := make(map[string]*CategoryStat)

	for rows.Next() {
		var exeName string
		var secs int64
		if err := rows.Scan(&exeName, &secs); err != nil {
			return nil, err
		}

		mappedName, category := w.classifyByExe(exeName, "")
		if _, exists := catMap[category]; !exists {
			catMap[category] = &CategoryStat{Category: category}
		}
		cat := catMap[category]
		cat.TotalSecs += secs
		
		// Aggregate by mappedName inside the category
		found := false
		for i := range cat.Apps {
			if cat.Apps[i].ExeName == mappedName {
				cat.Apps[i].TotalSecs += secs
				found = true
				break
			}
		}
		if !found {
			cat.Apps = append(cat.Apps, AppStat{ExeName: mappedName, TotalSecs: secs})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Convert map values to a slice and sort by descending total time.
	result := make([]CategoryStat, 0, len(catMap))
	for _, stat := range catMap {
		result = append(result, *stat)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].TotalSecs > result[j].TotalSecs
	})

	return result, nil
}

// GetProductivityHistory queries SQLite for productivity data over the past N days.
// Called from JS via: const history = await window.go.main.App.GetProductivityHistory(7)
func (w *Watcher) GetProductivityHistory(days int) ([]HistoryEntry, error) {
	rows, err := w.db.QueryContext(w.ctx, `
		SELECT date(logged_at, 'localtime') AS log_date, app_name, SUM(duration_s) AS total_secs
		FROM   productivity_logs
		WHERE  date(logged_at, 'localtime') >= date('now', 'localtime', ?)
		GROUP  BY log_date, app_name
		ORDER  BY log_date DESC, total_secs DESC
	`, fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// map[date]map[category]*CategoryStat
	dateMap := make(map[string]map[string]*CategoryStat)

	for rows.Next() {
		var logDate string
		var exeName string
		var secs int64
		if err := rows.Scan(&logDate, &exeName, &secs); err != nil {
			return nil, err
		}

		if _, exists := dateMap[logDate]; !exists {
			dateMap[logDate] = make(map[string]*CategoryStat)
		}
		catMap := dateMap[logDate]

		_, category := w.classifyByExe(exeName, "")
		if _, exists := catMap[category]; !exists {
			catMap[category] = &CategoryStat{Category: category}
		}
		cat := catMap[category]
		cat.TotalSecs += secs
		cat.Apps = append(cat.Apps, AppStat{ExeName: exeName, TotalSecs: secs})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var history []HistoryEntry
	for date, catMap := range dateMap {
		var stats []CategoryStat
		for _, stat := range catMap {
			stats = append(stats, *stat)
		}
		sort.Slice(stats, func(i, j int) bool {
			return stats[i].TotalSecs > stats[j].TotalSecs
		})
		history = append(history, HistoryEntry{Date: date, Stats: stats})
	}

	sort.Slice(history, func(i, j int) bool {
		return history[i].Date > history[j].Date // newest first
	})

	return history, nil
}

// GetUniqueApps returns a list of all distinct application names seen in the logs.
func (w *Watcher) GetUniqueApps() ([]string, error) {
	rows, err := w.db.QueryContext(w.ctx, `SELECT DISTINCT app_name FROM productivity_logs ORDER BY app_name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		apps = append(apps, name)
	}
	return apps, rows.Err()
}

// GetUnmappedPrograms returns a list of executables from productivity_logs that don't match any existing rules.
func (w *Watcher) GetUnmappedPrograms() ([]string, error) {
	rows, err := w.db.QueryContext(w.ctx, `SELECT DISTINCT app_name, window_title FROM productivity_logs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var unmapped []string
	seen := make(map[string]bool)

	for rows.Next() {
		var exeName, windowTitle string
		if err := rows.Scan(&exeName, &windowTitle); err != nil {
			return nil, err
		}
		if exeName == "" {
			continue
		}
		_, cat := w.classifyByExe(exeName, windowTitle)
		if cat == "Other" && !seen[exeName] {
			seen[exeName] = true
			unmapped = append(unmapped, exeName)
		}
	}
	sort.Strings(unmapped)
	return unmapped, rows.Err()
}

// GetDetailedLogs queries SQLite for all raw session logs over the past N days.
func (w *Watcher) GetDetailedLogs(days int) ([]DetailedLog, error) {
	rows, err := w.db.QueryContext(w.ctx, `
		SELECT 
			app_name, 
			window_title,
			date(logged_at, 'localtime') AS log_date, 
			datetime(logged_at, '-' || duration_s || ' seconds', 'localtime') AS started_at,
			datetime(logged_at, 'localtime') AS ended_at,
			duration_s
		FROM productivity_logs
		WHERE date(logged_at, 'localtime') >= date('now', 'localtime', ?)
		ORDER BY logged_at DESC
	`, fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []DetailedLog
	for rows.Next() {
		var log DetailedLog
		var title string
		if err := rows.Scan(&log.AppName, &title, &log.Date, &log.StartedAt, &log.EndedAt, &log.DurationSecs); err != nil {
			return nil, err
		}
		mappedName, cat := w.classifyByExe(log.AppName, title)
		log.AppName = mappedName
		log.Category = cat
		logs = append(logs, log)
	}
	return logs, rows.Err()
}

// GetProductivityHistoryByApp queries SQLite for productivity data for a specific app over the past N days.
func (w *Watcher) GetProductivityHistoryByApp(appName string, days int) ([]AppHistoryEntry, error) {
	rows, err := w.db.QueryContext(w.ctx, `
		SELECT date(logged_at, 'localtime') AS log_date, SUM(duration_s) AS total_secs
		FROM   productivity_logs
		WHERE  app_name = ? AND date(logged_at, 'localtime') >= date('now', 'localtime', ?)
		GROUP  BY log_date
		ORDER  BY log_date DESC
	`, appName, fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []AppHistoryEntry
	for rows.Next() {
		var entry AppHistoryEntry
		if err := rows.Scan(&entry.Date, &entry.TotalSecs); err != nil {
			return nil, err
		}
		history = append(history, entry)
	}
	return history, rows.Err()
}

// ── Core Polling Loop ──────────────────────────────────────────────────────────

// run is the private main loop, always executed inside a goroutine (via Start).
//
// ┌─ select mechanics for Python devs ─────────────────────────────────────────┐
// │                                                                             │
// │  for {                                                                      │
// │      select {                 ← blocks until ONE case fires                 │
// │      case <-ticker.C:         ← 5-second interval tick                     │
// │          w.poll()                                                           │
// │      case <-w.ctx.Done():     ← Wails signals shutdown                     │
// │          w.flushSession()     ← save the in-progress session               │
// │          return               ← goroutine exits cleanly                    │
// │      }                                                                      │
// │  }                                                                          │
// │                                                                             │
// │  Python asyncio equivalent:                                                 │
// │  done, _ = await asyncio.wait(                                              │
// │      [ticker_task, shutdown_event],                                         │
// │      return_when=asyncio.FIRST_COMPLETED                                    │
// │  )                                                                          │
// └─────────────────────────────────────────────────────────────────────────────┘
func (w *Watcher) run() {
	// Poll immediately on startup — don't wait 5 seconds for the first reading.
	w.poll()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop() // prevents goroutine leak inside the ticker

	for {
		select {
		case <-ticker.C:
			w.poll()

		case <-w.ctx.Done():
			// App is shutting down. Flush the current active session so the
			// last work period isn't lost from the productivity record.
			w.flushSession()
			log.Println("[Watcher] Context cancelled. Telemetry engine stopped cleanly.")
			return
		}
	}
}

// poll reads the current foreground window from Windows APIs and processes
// any state changes. This is the hot path — called every 5 seconds.
func (w *Watcher) poll() {
	// Step 1: Read the Windows API state — no lock needed here since
	// getForegroundWindowInfo only reads OS state, not our struct fields.
	title, exeName := getForegroundWindowInfo()

	// Filter out system windows with no title or unknown process (taskbar, desktop, etc.)
	if title == "" || exeName == "" {
		return
	}

	_, category := w.classifyByExe(exeName, title)
	now := time.Now()

	// ── Critical Section: Update in-memory session state ──────────────────────
	// We hold the write lock only for the in-memory update, NOT during DB I/O.
	// This minimizes lock contention — GetCurrentWindow() can proceed quickly.
	w.mu.Lock()

	// Same application as the current tracked session?
	// Update the title (it may change, e.g., different VS Code file)
	// but keep the session's startedAt unchanged — the clock keeps running.
	if w.current != nil && strings.EqualFold(w.current.exeName, exeName) {
		w.current.title = title // update title in-place
		w.mu.Unlock()
		return // nothing else to do
	}

	// Window HAS changed — capture the old session data before overwriting.
	// We copy the data out (not the pointer) so we can use it after releasing the lock.
	var (
		oldSession  *activeSession
		oldDuration time.Duration
	)
	if w.current != nil {
		oldDuration = now.Sub(w.current.startedAt)
		// Make a copy of the session so we can safely use it after w.mu.Unlock()
		// Analogy: Python's copy.copy(obj) — creating a shallow value copy.
		sessionCopy := *w.current
		oldSession = &sessionCopy
	}

	// Install the new session
	w.current = &activeSession{
		title:     title,
		exeName:   exeName,
		category:  category,
		startedAt: now,
	}
	w.mu.Unlock()
	// ── End Critical Section ───────────────────────────────────────────────────

	// I/O happens outside the lock — DB writes and event emission can be slow
	// and should never block a waiting GetCurrentWindow() caller.

	// Log the OLD session if it lasted at least 5 seconds (one poll interval).
	// Shorter sessions are noise — the user likely just glanced at the window.
	if oldSession != nil && oldDuration >= 5*time.Second {
		w.logSessionToDB(oldSession, oldDuration)
	}

	log.Printf("[Watcher] Focus → [%s] %s  (%s)", category, title, exeName)

	// Emit the window-changed event to the JavaScript frontend.
	// JS listener: window.runtime.EventsOn("watcher:window_changed", (info) => {...})
	runtime.EventsEmit(w.ctx, "watcher:window_changed", WindowInfo{
		Title:     title,
		ExeName:   exeName,
		Category:  category,
		StartedAt: now.Format(time.RFC3339),
	})
}

// flushSession writes the current active session to the DB on app shutdown.
// Called from run() after ctx.Done() fires, so no concurrent poll() is running.
func (w *Watcher) flushSession() {
	w.mu.Lock()
	session := w.current
	w.current = nil
	w.mu.Unlock()

	if session == nil {
		return
	}
	duration := time.Since(session.startedAt)
	if duration >= 5*time.Second {
		w.logSessionToDB(session, duration)
		log.Printf("[Watcher] Flushed final session: %s (%s)", session.exeName, duration.Round(time.Second))
	}
}

// logSessionToDB persists a completed focus session into the productivity_logs table.
// Must NOT be called while w.mu is held — DB I/O must stay outside the lock.
func (w *Watcher) logSessionToDB(session *activeSession, duration time.Duration) {
	durationSecs := int64(duration.Seconds())
	_, err := w.db.Exec(
		`INSERT INTO productivity_logs (app_name, window_title, duration_s, logged_at)
		 VALUES (?, ?, ?, ?)`,
		session.exeName,
		session.title,
		durationSecs,
		// Log the session's start time (not end time) — consistent with the
		// mental model "you were in VS Code starting at 10:30".
		session.startedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		// Non-fatal: the monitoring loop must stay alive even if individual writes fail.
		log.Printf("[Watcher] Warning: DB write failed for session %q: %v", session.exeName, err)
	} else {
		log.Printf("[Watcher] Persisted: %s [%s] for %ds", session.exeName, session.category, durationSecs)
	}
}

// ── Windows API Helpers ────────────────────────────────────────────────────────
//
// These functions wrap raw Win32 API calls using Go's syscall package.
//
// ┌─ unsafe.Pointer deep-dive ──────────────────────────────────────────────────┐
// │                                                                             │
// │  Win32 C functions expect raw memory pointers (LPWSTR, LPDWORD, etc.).     │
// │  Go's type system doesn't know about these C types, so we use:             │
// │                                                                             │
// │    uintptr(unsafe.Pointer(&goVariable))                                     │
// │                                                                             │
// │  This converts a Go pointer to an integer address that the C function      │
// │  can write into. The Go runtime's GC may move objects in memory, but       │
// │  it guarantees the object stays pinned for the duration of a Call().       │
// │                                                                             │
// │  CRITICAL RULE: NEVER store the uintptr in a variable before Call():       │
// │    ❌ ptr := uintptr(unsafe.Pointer(&buf[0]))  // GC may move buf here!    │
// │    ❌ proc.Call(ptr)                            // ptr is now stale         │
// │    ✅ proc.Call(uintptr(unsafe.Pointer(&buf[0]))) // conversion + call atomic│
// │                                                                             │
// │  Python analogy: ctypes.byref(var) or ctypes.cast(ptr, ctypes.POINTER(...))│
// └─────────────────────────────────────────────────────────────────────────────┘

// getForegroundWindowInfo returns the title and process name of the current
// foreground window. Returns ("", "") if the desktop or an inaccessible window
// is in the foreground.
//
// Win32 call chain:
//
//	GetForegroundWindow() → HWND
//	GetWindowTextW(hwnd)  → title string
//	GetWindowThreadProcessId(hwnd) → pid
//	OpenProcess(pid)              → process handle
//	QueryFullProcessImageNameW()  → full exe path → basename
func getForegroundWindowInfo() (title, exeName string) {
	// GetForegroundWindow returns the HWND (window handle) — an opaque integer.
	// In Go we represent it as uintptr. A return value of 0 means no foreground window.
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return "", ""
	}

	// GetWindowTextW writes the UTF-16 window title into our buffer.
	// We allocate a []uint16 slice (UTF-16 code units) as the target buffer.
	// The function takes: (hwnd, *buffer, bufferLength) and returns charsCopied.
	titleBuf := make([]uint16, 512)
	titleLen, _, _ := procGetWindowTextW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&titleBuf[0])), // pointer to first element of slice
		uintptr(len(titleBuf)),                // max characters to write
	)
	if titleLen == 0 {
		// Empty title — this is typically the desktop or system windows.
		return "", ""
	}
	// syscall.UTF16ToString converts a UTF-16 []uint16 slice to a Go string.
	// Python analogy: buffer.value.decode('utf-16-le') with ctypes wchar buffers.
	title = syscall.UTF16ToString(titleBuf[:titleLen])

	// GetWindowThreadProcessId fills our `pid` variable via an OUT pointer.
	// The return value is the thread ID (which we don't need).
	var pid uint32
	procGetWindowThreadProcessId.Call(
		hwnd,
		uintptr(unsafe.Pointer(&pid)), // OUT: populated by the function
	)
	if pid == 0 {
		return title, "" // we have a title but can't identify the process
	}

	exeName = getProcessName(pid)
	return title, exeName
}

// getProcessName resolves a PID to its executable basename (e.g., "Code.exe").
// Returns "" if the process cannot be accessed (system processes, insufficient rights).
func getProcessName(pid uint32) string {
	// OpenProcess acquires a handle to the process so we can query its properties.
	// PROCESS_QUERY_LIMITED_INFORMATION (0x1000) is the minimum access right needed
	// for QueryFullProcessImageNameW and works without elevation for user processes.
	handle, _, _ := procOpenProcess.Call(
		processQueryLimitedInformation,
		0,            // bInheritHandle = FALSE (child processes don't inherit this handle)
		uintptr(pid), // the target PID
	)
	if handle == 0 {
		return "" // access denied (system/protected process)
	}
	// CloseHandle is critical — leaking handles causes resource exhaustion over time.
	// `defer` guarantees it runs when getProcessName returns, regardless of the path.
	// Python analogy: using `with open(...)` — the handle is released on exit.
	defer procCloseHandle.Call(handle)

	// QueryFullProcessImageNameW writes the full Win32 path (e.g., C:\Program Files\...)
	// into our buffer. `size` is an IN/OUT parameter: we pass the buffer capacity,
	// and the function overwrites it with the actual number of characters written.
	buf := make([]uint16, 512) // 512 > MAX_PATH (260); handles long-path edge cases
	size := uint32(len(buf))
	ret, _, _ := procQueryFullProcessImageNameW.Call(
		handle,
		0,                                  // dwFlags = 0 → Win32 path format
		uintptr(unsafe.Pointer(&buf[0])),   // OUT: receives the exe path
		uintptr(unsafe.Pointer(&size)),     // IN/OUT: buffer size → chars written
	)
	if ret == 0 {
		return "" // query failed (process may have exited between OpenProcess and here)
	}

	// Convert the UTF-16 buffer to a Go string, then extract just the filename.
	// filepath.Base("C:\\Program Files\\Microsoft VS Code\\Code.exe") → "Code.exe"
	// Python analogy: os.path.basename(full_path)
	fullPath := syscall.UTF16ToString(buf[:size])
	return filepath.Base(fullPath)
}
