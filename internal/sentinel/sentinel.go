// Package sentinel implements The Sentinel — Sentinel-OS's uptime monitoring engine.
//
// Architecture overview:
//
//	startup() in app.go
//	    └─ sentinel.New(...)  → constructs the engine
//	    └─ s.Start()          → launches ONE background goroutine (s.run)
//	           │
//	           ├─ on every 30s tick → s.checkAll()
//	           │       └─ spawns N goroutines (one per target, concurrently)
//	           │               └─ each sends a CheckResult to a buffered channel
//	           │       └─ range over channel → s.processResult() for each result
//	           │               └─ writes to SQLite
//	           │               └─ emits Wails event IF state changed (UP↔DOWN)
//	           │
//	           └─ on ctx.Done() (app shutdown) → return (goroutine exits cleanly)
package sentinel

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ── Types ──────────────────────────────────────────────────────────────────────

// TargetKind is a string enum distinguishing the two check protocols.
// Using a named type (vs raw string) prevents typos at call sites — the
// compiler rejects `sentinel.Target{Kind: "htpp"}` at build time.
type TargetKind string

const (
	KindTCP  TargetKind = "tcp"  // Raw TCP dial — checks if port is open
	KindHTTP TargetKind = "http" // HTTP GET — checks for 2xx/3xx response
)

// Target describes a single endpoint to monitor.
// Python analogy: a dataclass or NamedTuple defining what to check.
type Target struct {
	ID      int64      `json:"id"`
	Name    string     `json:"name"`
	Address string     `json:"address"` // "localhost:8000" for TCP, "https://myapp.com" for HTTP
	Kind    TargetKind `json:"kind"`    // KindTCP or KindHTTP
}

// checkResult is an internal struct holding the outcome of a single health probe.
// It is never exposed to JavaScript — it flows through Go channels only.
type checkResult struct {
	target    Target
	isUp      bool
	latencyMs int64  // time from dial-start to completion, in milliseconds
	errMsg    string // non-empty only on failure
}

// StatusSnapshot is a JSON-serializable snapshot of one target's current health.
// This IS exported to JavaScript — every exported field becomes a JSON key.
// Wails auto-serializes this to { "address": "...", "isUp": true, ... }
type StatusSnapshot struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Address   string `json:"address"`
	Kind      string `json:"kind"`
	IsUp      bool   `json:"isUp"`
	LatencyMs int64  `json:"latencyMs"`
	CheckedAt string `json:"checkedAt"` // RFC3339 timestamp string
}

// ── Sentinel Engine ────────────────────────────────────────────────────────────

// Sentinel is the uptime monitoring engine. It holds all runtime state needed
// to run the monitoring loop, emit events, and log results.
//
// Go concurrency note: Sentinel is designed to be used from multiple goroutines
// concurrently. The `mu` RWMutex protects the mutable fields (targets, statusMap).
// *sql.DB is already concurrency-safe internally — no mutex needed for DB calls.
type Sentinel struct {
	// ctx is the Wails runtime context — it serves DUAL PURPOSE:
	//   1. Passed to runtime.EventsEmit() to push events to the frontend.
	//   2. ctx.Done() returns a channel that closes when Wails shuts the app down.
	//      We listen on this channel to exit the monitoring loop cleanly.
	//
	// Python analogy:
	//   - Purpose 1: like Django's `request` object giving access to the runtime env.
	//   - Purpose 2: like a threading.Event that gets .set() when shutdown is requested.
	ctx context.Context

	db *sql.DB // shared connection pool — safe for concurrent goroutine access

	mu        sync.RWMutex  // guards `targets` and `statusMap`
	targets   []Target       // list of endpoints to monitor
	statusMap map[string]bool // last known UP(true)/DOWN(false) per address

	// httpClient is reused across all HTTP checks to leverage connection pooling.
	// Creating a new http.Client per request (like requests.get() in Python) is
	// wasteful — a shared client with a timeout is the correct Go idiom.
	httpClient *http.Client
}

// New constructs a Sentinel engine. Call Start() after construction to begin monitoring.
//
//	ctx     — the Wails runtime context from app.startup()
//	db      — the shared SQLite connection pool
func New(ctx context.Context, db *sql.DB) *Sentinel {
	s := &Sentinel{
		ctx:       ctx,
		db:        db,
		statusMap: make(map[string]bool),
		httpClient: &http.Client{
			// 10-second timeout prevents a single slow host from blocking the
			// entire check round. Python equivalent: requests.get(url, timeout=10)
			Timeout: 10 * time.Second,
		},
	}
	s.loadTargetsFromDB()
	return s
}

// loadTargetsFromDB reads all targets from the database.
func (s *Sentinel) loadTargetsFromDB() {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT id, name, address, kind FROM uptime_targets`)
	if err != nil {
		log.Printf("[Sentinel] Error loading targets: %v", err)
		return
	}
	defer rows.Close()

	var newTargets []Target
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.ID, &t.Name, &t.Address, &t.Kind); err == nil {
			newTargets = append(newTargets, t)
		}
	}
	s.targets = newTargets
}

// ── Public API ─────────────────────────────────────────────────────────────────

// Start launches the monitoring loop in a background goroutine and returns immediately.
//
// ┌─ Go concurrency analogy for Python devs ───────────────────────────────────┐
// │                                                                             │
// │  Go goroutine:          Python equivalent:                                  │
// │  ─────────────          ──────────────────                                  │
// │  go s.run()         ≈   t = threading.Thread(target=s.run, daemon=True)    │
// │                          t.start()                                          │
// │                                                                             │
// │  A goroutine is NOT a thread — it's a lightweight cooperative unit         │
// │  scheduled by the Go runtime (M:N threading). You can spawn thousands      │
// │  for near-zero cost. `go f()` is the entire syntax to launch one.          │
// │                                                                             │
// │  The `go` keyword is non-blocking. Start() returns instantly, and          │
// │  s.run() proceeds in the background on a separate scheduler slot.          │
// └─────────────────────────────────────────────────────────────────────────────┘
func (s *Sentinel) Start() {
	go s.run()
	log.Println("[Sentinel] Engine started. Monitoring", len(s.targets), "target(s).")
}

// ReloadTargets triggers a fresh fetch of targets from the database.
func (s *Sentinel) ReloadTargets() {
	s.loadTargetsFromDB()
}

// GetCurrentStatuses returns a point-in-time snapshot of all known target statuses.
// Called by App.GetMonitorStatuses() which is bound to JavaScript.
func (s *Sentinel) GetCurrentStatuses() []StatusSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now().Format(time.RFC3339)
	snapshots := make([]StatusSnapshot, 0, len(s.targets))
	for _, t := range s.targets {
		isUp := s.statusMap[t.Address] // zero-value false if not yet checked
		snapshots = append(snapshots, StatusSnapshot{
			ID:        t.ID,
			Name:      t.Name,
			Address:   t.Address,
			Kind:      string(t.Kind),
			IsUp:      isUp,
			CheckedAt: now,
		})
	}
	return snapshots
}

// ── Core Monitoring Loop ───────────────────────────────────────────────────────

// run is the private main loop. It is always executed inside a goroutine (via Start).
//
// ┌─ Context cancellation analogy for Python devs ─────────────────────────────┐
// │                                                                             │
// │  Go:                         Python:                                        │
// │  ─────────────────────────   ─────────────────────────                      │
// │  ctx.Done()              ≈   threading.Event()                              │
// │  <-ctx.Done()            ≈   event.wait() (blocking receive on channel)     │
// │  select { case <-ctx.Done() } ≈ if stop_event.is_set(): break              │
// │                                                                             │
// │  ctx.Done() returns a channel. In Go, receiving from a closed channel       │
// │  returns immediately. Wails closes this channel when the app shuts down.   │
// │  The `select` statement waits on MULTIPLE channels simultaneously,          │
// │  like asyncio.wait([task1, task2], return_when=FIRST_COMPLETED).           │
// └─────────────────────────────────────────────────────────────────────────────┘
func (s *Sentinel) run() {
	// Run an immediate check on startup so the dashboard isn't blank for 30s.
	s.checkAll()

	// time.Ticker fires on a fixed interval, sending to ticker.C each time.
	// Python analogy: like `schedule.every(30).seconds.do(checkAll)` or
	// asyncio's `asyncio.sleep(30)` in a while loop — but without drift.
	//
	// CRITICAL: time.Sleep(30s) in a loop is wrong for clean shutdown because
	// it cannot be interrupted. A Ticker + select can be cancelled instantly.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop() // always clean up the ticker's internal goroutine

	for {
		// select blocks until ONE of its cases is ready.
		// This is the idiomatic Go pattern for "do work OR exit on shutdown".
		select {
		case <-ticker.C:
			// The 30-second tick fired. Run all health checks.
			s.checkAll()

		case <-s.ctx.Done():
			// The Wails context was cancelled — the app is shutting down.
			// Returning from this function exits the goroutine cleanly.
			// Python analogy: except KeyboardInterrupt → cleanup → sys.exit()
			log.Println("[Sentinel] Context cancelled. Monitoring loop stopped cleanly.")
			return
		}
	}
}

// checkAll launches one goroutine per target and collects all results via a channel.
//
// Concurrency pattern used here:
//
//	goroutine per target → buffered channel → serial result processing
//
// The channel buffer is pre-sized to len(targets), so every goroutine can send
// its result without blocking, even if the consumer isn't ready yet.
// A separate goroutine calls wg.Wait() and then closes the channel, which signals
// `for range results` to stop iterating.
func (s *Sentinel) checkAll() {
	// Take a read-lock snapshot of targets to avoid holding the lock during I/O.
	s.mu.RLock()
	targets := make([]Target, len(s.targets))
	copy(targets, s.targets)
	s.mu.RUnlock()

	if len(targets) == 0 {
		return
	}

	// Buffered channel sized to the number of targets.
	// Buffering ensures goroutines never block on send, even if we're slow to receive.
	results := make(chan checkResult, len(targets))

	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		// We capture `t` as a parameter to the goroutine closure — NOT as a closure
		// over the loop variable. This is critical in Go: loop variables are reused
		// per iteration, so a direct closure capture would give all goroutines the
		// LAST value of `t`. Passing it as a parameter creates a private copy.
		//
		// Python analogy: this is the same bug as:
		//   fns = [lambda: print(i) for i in range(3)]  # all print 2!
		//   # Fix: lambda i=i: print(i)
		go func(target Target) {
			defer wg.Done()
			results <- s.check(target)
		}(t)
	}

	// Close the results channel once ALL goroutines have sent their result.
	// This runs in its own goroutine so it doesn't block checkAll().
	go func() {
		wg.Wait()
		close(results)
	}()

	// Range over the channel. Blocks until the channel is closed (all checks done).
	for r := range results {
		s.processResult(r)
	}
}

// check dispatches to the correct protocol implementation based on target kind.
func (s *Sentinel) check(t Target) checkResult {
	start := time.Now()
	switch t.Kind {
	case KindTCP:
		return s.checkTCP(t, start)
	case KindHTTP:
		return s.checkHTTP(t, start)
	default:
		return checkResult{target: t, isUp: false, errMsg: fmt.Sprintf("unknown target kind: %q", t.Kind)}
	}
}

// checkTCP attempts to open a raw TCP connection to the address.
// A successful dial means the port is open and accepting connections.
// This is equivalent to: `socket.create_connection((host, port), timeout=10)`
func (s *Sentinel) checkTCP(t Target, start time.Time) checkResult {
	// net.DialTimeout opens a TCP connection with a deadline.
	// Returns: (net.Conn, error). If error is nil, the port is UP.
	conn, err := net.DialTimeout("tcp", t.Address, 10*time.Second)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return checkResult{target: t, isUp: false, latencyMs: latency, errMsg: err.Error()}
	}
	conn.Close() // we only needed the handshake to confirm the port is open
	return checkResult{target: t, isUp: true, latencyMs: latency}
}

// checkHTTP sends an HTTP GET and checks for a non-error response status (< 400).
// Uses the shared httpClient for connection pool reuse.
func (s *Sentinel) checkHTTP(t Target, start time.Time) checkResult {
	resp, err := s.httpClient.Get(t.Address)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		// Network error: DNS failure, connection refused, timeout, etc.
		return checkResult{target: t, isUp: false, latencyMs: latency, errMsg: err.Error()}
	}
	defer resp.Body.Close()

	// We consider 2xx and 3xx as "UP". 4xx/5xx means the server is there
	// but reporting an error — we classify this as DOWN for monitoring purposes.
	isUp := resp.StatusCode < 400
	errMsg := ""
	if !isUp {
		errMsg = fmt.Sprintf("HTTP %d %s", resp.StatusCode, resp.Status)
	}
	return checkResult{target: t, isUp: isUp, latencyMs: latency, errMsg: errMsg}
}

// ── Result Processing ──────────────────────────────────────────────────────────

// processResult handles a single completed health check:
//  1. Always writes the result to the SQLite uptime_logs table.
//  2. Compares against the last known status. If the state CHANGED (UP→DOWN or DOWN→UP),
//     emits a Wails event to the JavaScript frontend.
//
// Only emitting on STATE CHANGE (not on every tick) is critical — it prevents
// flooding the frontend with redundant events every 30 seconds.
func (s *Sentinel) processResult(r checkResult) {
	now := time.Now()
	address := r.target.Address
	status := "DOWN"
	if r.isUp {
		status = "UP"
	}

	// 1. Always persist the check result to SQLite.
	s.logToDatabase(address, status, r.latencyMs, now)

	// 2. Check for state change — requires a write lock since we may update statusMap.
	s.mu.Lock()
	prevIsUp, wasSeen := s.statusMap[address]
	stateChanged := !wasSeen || (prevIsUp != r.isUp)
	s.statusMap[address] = r.isUp // update the last-known state
	s.mu.Unlock()

	// Always log for observability, but use different log levels.
	if r.isUp {
		log.Printf("[Sentinel] ✓ UP   %s  (%dms)", address, r.latencyMs)
	} else {
		log.Printf("[Sentinel] ✗ DOWN %s  (%dms) — %s", address, r.latencyMs, r.errMsg)
	}

	// 3. Only emit a frontend event when the state transitions.
	if stateChanged {
		// runtime.EventsEmit broadcasts a named event with an arbitrary payload to JS.
		// In JavaScript: window.runtime.EventsOn("sentinel:status_change", (data) => {...})
		//
		// The payload (StatusSnapshot) is auto-serialized to JSON by Wails.
		// Python analogy: like Django Channels broadcasting a WebSocket message,
		// or emitting a custom DOM event with event.detail = payload.
		runtime.EventsEmit(s.ctx, "sentinel:status_change", StatusSnapshot{
			ID:        r.target.ID,
			Name:      r.target.Name,
			Address:   address,
			Kind:      string(r.target.Kind),
			IsUp:      r.isUp,
			LatencyMs: r.latencyMs,
			CheckedAt: now.Format(time.RFC3339),
		})

		if !r.isUp {
			log.Printf("[Sentinel] ⚡ STATE CHANGE → DOWN: %s", address)
		} else {
			log.Printf("[Sentinel] ⚡ STATE CHANGE → UP:   %s", address)
		}
	}
}

// logToDatabase inserts a single uptime check record into the SQLite table.
// The latency is stored as a nullable integer — it is meaningful even for
// failed checks (it represents time-until-timeout, showing how long we waited).
func (s *Sentinel) logToDatabase(address, status string, latencyMs int64, t time.Time) {
	_, err := s.db.Exec(
		`INSERT INTO uptime_logs (target, status, latency_ms, checked_at) VALUES (?, ?, ?, ?)`,
		address,
		status,
		latencyMs,
		t.UTC().Format(time.RFC3339),
	)
	if err != nil {
		// Non-fatal: log the error but do NOT crash the monitoring loop.
		// The monitoring engine must remain alive even if individual DB writes fail.
		log.Printf("[Sentinel] Warning: failed to write to uptime_logs: %v", err)
	}
}
