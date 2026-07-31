// Package snapshot implements The Snapshot Engine — Sentinel-OS's deterministic
// local versioning system. It provides content-addressable file storage, SHA-1
// tree hashing, and LCS-based line-level diffing — all without Git dependency.
//
// Architecture overview:
//
//	startup() in app.go
//	    └─ snapshot.New(ctx, db) → constructs engine
//	    └─ e.Start()             → spawns ONE background worker goroutine
//	           │
//	           ├─ receives job via e.jobs channel
//	           │       └─ e.doSnapshot(projectPath, message)
//	           │               ├─ walkProject()     → []fileRecord (hash + content)
//	           │               ├─ computeTreeHash() → deterministic commit hash
//	           │               ├─ persistSnapshot() → SQLite transaction
//	           │               └─ runtime.EventsEmit("snapshot:created", ...)
//	           │
//	           └─ ctx.Done() → worker goroutine exits cleanly
//
// Content-addressable storage design (mirrors Git's object model):
//
//	snapshot_blobs: hash → content (BLOB)   ← each unique file version stored ONCE
//	snapshot_files: snapshot_id → file records (relPath, hash, size, lines)
//	snapshots:      id → metadata (project, commitHash, message, timestamp)
//
// Deduplication: if file X appears unchanged across 10 snapshots, its content
// is stored exactly once in snapshot_blobs. snapshot_files holds 10 references.
package snapshot

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ── Constants & Skip Rules ─────────────────────────────────────────────────────

// errBinaryFile is returned by hashFile when binary content is detected.
// Callers should skip the file silently — not a fatal error.
var errBinaryFile = fmt.Errorf("binary file: skipped")

// ignoreDirs is the set of directory basenames to prune during a directory walk.
// When WalkDir encounters one of these names, it skips the ENTIRE subtree.
//
// Python analogy: `dirs[:] = [d for d in dirs if d not in ignoreDirs]`
// inside `for root, dirs, files in os.walk(projectPath):`
var ignoreDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true,
	"node_modules": true, "__pycache__": true, ".venv": true, "venv": true,
	"vendor": true, "dist": true, "build": true, "target": true,
	".idea": true, ".vscode": true, ".sentinel": true,
	"bin": true, "obj": true, ".cache": true, ".mypy_cache": true,
}

// binaryExtensions maps file extensions that are always skipped.
// Extension check is O(1) and avoids opening the file at all.
var binaryExtensions = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".lib": true, ".o": true, ".a": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".ico": true, ".webp": true,
	".mp4": true, ".mp3": true, ".wav": true, ".avi": true, ".mov": true, ".mkv": true,
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".7z": true, ".rar": true,
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".sqlite": true, ".db": true, ".pyc": true, ".class": true, ".jar": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".svg": true, // SVG can be text but is usually large and uninteresting to diff
}

const (
	// maxFileSizeBytes: files larger than this are skipped to avoid bloating the DB.
	maxFileSizeBytes = 5 * 1024 * 1024 // 5 MB

	// maxLCSLines: combined old+new line count above which we use fast approximation
	// instead of the O(m×n) LCS algorithm. Prevents slowdowns on very large files.
	maxLCSLines = 5_000
)

// ── Exported Types (JS-facing) ─────────────────────────────────────────────────

// SnapshotInfo is the JSON-serializable metadata for one snapshot (commit).
// Returned by CreateSnapshot and GetSnapshotHistory.
type SnapshotInfo struct {
	ID          int64  `json:"id"`
	ProjectPath string `json:"projectPath"`
	CommitHash  string `json:"commitHash"` // 7-char abbreviated hash (like `git log --short`)
	FullHash    string `json:"fullHash"`   // full 40-char SHA-1 hex string
	Message     string `json:"message"`
	FileCount   int    `json:"fileCount"`
	TotalBytes  int64  `json:"totalBytes"`
	CreatedAt   string `json:"createdAt"` // RFC3339 timestamp
}

// DiffChunk represents a piece of a diff
type DiffChunk struct {
	Type int    `json:"type"` // -1: Delete, 0: Equal, 1: Insert
	Text string `json:"text"`
}

// FileDiff holds line-level change stats and diff chunks for one modified file between two snapshots.
type FileDiff struct {
	RelPath      string      `json:"relPath"`
	LinesAdded   int         `json:"linesAdded"`
	LinesDeleted int         `json:"linesDeleted"`
	Chunks       []DiffChunk `json:"chunks"`
}

// DiffSummary is the aggregate change count across all files in a diff.
type DiffSummary struct {
	FilesAdded    int `json:"filesAdded"`
	FilesDeleted  int `json:"filesDeleted"`
	FilesModified int `json:"filesModified"`
	LinesAdded    int `json:"linesAdded"`
	LinesDeleted  int `json:"linesDeleted"`
}

// DiffResult is the complete result of comparing two snapshots.
// Returned by GetDiff and serialized to JS as a structured object.
type DiffResult struct {
	OldSnapshotID int64      `json:"oldSnapshotId"`
	NewSnapshotID int64      `json:"newSnapshotId"`
	AddedFiles    []string   `json:"addedFiles"`    // files present in new, absent in old
	DeletedFiles  []string   `json:"deletedFiles"`  // files present in old, absent in new
	ModifiedFiles []FileDiff `json:"modifiedFiles"` // files in both but with different hashes
	Summary       DiffSummary `json:"summary"`
}

// ── Internal Types ─────────────────────────────────────────────────────────────

// fileRecord holds the full data for one file during a snapshot operation.
// It is internal — never sent to JavaScript.
type fileRecord struct {
	relPath   string // forward-slash normalized path relative to project root
	hash      string // SHA-1 hex of file content
	sizeBytes int64
	lineCount int
	content   []byte // raw content held for blob insertion, then GC'd after commit
}

// snapshotFileRow is a lightweight DB record from the snapshot_files table.
type snapshotFileRow struct {
	relPath   string
	fileHash  string
	lineCount int
}

// job is one unit of work sent to the background worker goroutine.
type job struct {
	projectPath string
	message     string
	resultCh    chan<- jobResult // send-only: worker writes result, caller reads it
}

// jobResult carries the worker's completed output back to the caller.
type jobResult struct {
	info SnapshotInfo
	err  error
}

// ── Engine ─────────────────────────────────────────────────────────────────────

// Engine is the snapshot engine with a channel-based background worker.
//
// The single worker goroutine serializes all snapshot operations, preventing
// concurrent writes or duplicate snapshots for the same project.
//
// ┌─ Channel-based worker analogy for Python devs ─────────────────────────────┐
// │                                                                             │
// │  Go:                                   Python:                              │
// │  ────────────────────────────          ──────────────────────────           │
// │  make(chan job, 5)                  ≈  queue.Queue(maxsize=5)               │
// │  e.jobs <- job{...}                ≈  q.put(job)                           │
// │  j := <-e.jobs                     ≈  job = q.get()                        │
// │  j.resultCh <- result              ≈  result_queue.put(result)             │
// │  r := <-resultCh                   ≈  result = result_queue.get()          │
// │                                                                             │
// │  Overall pattern:                                                           │
// │  Engine ≈ concurrent.futures.ThreadPoolExecutor(max_workers=1)             │
// │  TakeSnapshot ≈ executor.submit(doSnapshot, path, msg).result()            │
// └─────────────────────────────────────────────────────────────────────────────┘
type Engine struct {
	ctx  context.Context
	db   *sql.DB
	jobs chan job // buffered job queue; capacity = 5 pending requests
}

// New constructs the Engine. Call Start() once before any TakeSnapshot calls.
func New(ctx context.Context, db *sql.DB) *Engine {
	return &Engine{
		ctx:  ctx,
		db:   db,
		jobs: make(chan job, 5),
	}
}

// ── Public API ─────────────────────────────────────────────────────────────────

// Start launches the background worker goroutine and returns immediately.
func (e *Engine) Start() {
	go e.worker()
	log.Println("[Snapshot] Engine started. Worker goroutine live.")
}

// TakeSnapshot submits a snapshot request and BLOCKS until the worker completes it.
//
// This is safe to call from Wails JS bindings because Wails dispatches each
// bound method call in its own goroutine — the UI thread is never blocked.
// The JS caller receives the result as a resolved Promise once this returns.
func (e *Engine) TakeSnapshot(projectPath, message string) (SnapshotInfo, error) {
	resultCh := make(chan jobResult, 1) // buffer of 1 so worker never blocks on send

	// Submit the job — blocks if the job queue (capacity 5) is full.
	select {
	case e.jobs <- job{projectPath: projectPath, message: message, resultCh: resultCh}:
	case <-e.ctx.Done():
		return SnapshotInfo{}, fmt.Errorf("snapshot engine is shutting down")
	}

	// Wait for the result — blocks until the worker finishes processing.
	select {
	case r := <-resultCh:
		return r.info, r.err
	case <-e.ctx.Done():
		return SnapshotInfo{}, fmt.Errorf("shutdown while awaiting snapshot result")
	}
}

// GetHistory returns the N most recent snapshots for a given project path.
// Results are sorted newest-first. Delegated to by App.GetSnapshotHistory().
func (e *Engine) GetHistory(projectPath string, limit int) ([]SnapshotInfo, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	// LEFT JOIN with snapshot_files gives us aggregate file count and total size
	// without a separate query. COALESCE handles the case of an empty snapshot.
	rows, err := e.db.QueryContext(e.ctx, `
		SELECT s.id, s.project_path, s.commit_hash, COALESCE(s.message, ''),
		       s.created_at, COUNT(sf.id), COALESCE(SUM(sf.size_bytes), 0)
		FROM   snapshots s
		LEFT JOIN snapshot_files sf ON sf.snapshot_id = s.id
		WHERE  s.project_path = ?
		GROUP  BY s.id
		ORDER  BY s.id DESC
		LIMIT  ?
	`, projectPath, limit)
	if err != nil {
		return nil, fmt.Errorf("GetHistory query: %w", err)
	}
	defer rows.Close()

	var results []SnapshotInfo
	for rows.Next() {
		var info SnapshotInfo
		var fullHash string
		if err := rows.Scan(&info.ID, &info.ProjectPath, &fullHash,
			&info.Message, &info.CreatedAt, &info.FileCount, &info.TotalBytes); err != nil {
			return nil, err
		}
		info.FullHash = fullHash
		if len(fullHash) >= 7 {
			info.CommitHash = fullHash[:7]
		}
		results = append(results, info)
	}
	return results, rows.Err()
}

// GetDiff computes the file-level and line-level differences between two snapshots.
// oldID should be the earlier snapshot; newID the later one.
// Delegated to by App.GetSnapshotDiff().
func (e *Engine) GetDiff(oldID, newID int64) (DiffResult, error) {
	result := DiffResult{OldSnapshotID: oldID, NewSnapshotID: newID}

	oldFiles, err := e.loadSnapshotFiles(oldID)
	if err != nil {
		return result, fmt.Errorf("load old snapshot %d: %w", oldID, err)
	}
	newFiles, err := e.loadSnapshotFiles(newID)
	if err != nil {
		return result, fmt.Errorf("load new snapshot %d: %w", newID, err)
	}

	// ── Added files: present in new, absent in old ────────────────────────────
	// Python analogy: [p for p in new_files if p not in old_files]
	for path, newRow := range newFiles {
		if _, exists := oldFiles[path]; !exists {
			result.AddedFiles = append(result.AddedFiles, path)
			result.Summary.LinesAdded += newRow.lineCount
		}
	}

	// ── Deleted files: present in old, absent in new ──────────────────────────
	for path, oldRow := range oldFiles {
		if _, exists := newFiles[path]; !exists {
			result.DeletedFiles = append(result.DeletedFiles, path)
			result.Summary.LinesDeleted += oldRow.lineCount
		}
	}

	// ── Modified files: present in both but different SHA-1 hash ─────────────
	for path, oldRow := range oldFiles {
		newRow, exists := newFiles[path]
		if !exists || oldRow.fileHash == newRow.fileHash {
			continue // deleted (handled above) or unchanged
		}

		// Load both blobs from the content-addressable store and diff them.
		oldContent, _ := e.loadBlob(oldRow.fileHash)
		newContent, _ := e.loadBlob(newRow.fileHash)
		
		added, deleted := diffLineCount(oldContent, newContent, oldRow.lineCount+newRow.lineCount)
		
		dmp := diffmatchpatch.New()
		// Convert byte slices to strings for diffing
		diffs := dmp.DiffMain(string(oldContent), string(newContent), true)
		diffs = dmp.DiffCleanupSemantic(diffs)

		var chunks []DiffChunk
		for _, d := range diffs {
			chunks = append(chunks, DiffChunk{
				Type: int(d.Type),
				Text: d.Text,
			})
		}

		result.ModifiedFiles = append(result.ModifiedFiles, FileDiff{
			RelPath: path, LinesAdded: added, LinesDeleted: deleted, Chunks: chunks,
		})
		result.Summary.LinesAdded += added
		result.Summary.LinesDeleted += deleted
	}

	// Sort all output lists alphabetically for deterministic, readable output.
	sort.Strings(result.AddedFiles)
	sort.Strings(result.DeletedFiles)
	sort.Slice(result.ModifiedFiles, func(i, j int) bool {
		return result.ModifiedFiles[i].RelPath < result.ModifiedFiles[j].RelPath
	})

	result.Summary.FilesAdded = len(result.AddedFiles)
	result.Summary.FilesDeleted = len(result.DeletedFiles)
	result.Summary.FilesModified = len(result.ModifiedFiles)

	return result, nil
}

// ── Background Worker ──────────────────────────────────────────────────────────

// worker is the single goroutine that serializes all snapshot jobs.
// It reads from the jobs channel and writes results back via each job's resultCh.
func (e *Engine) worker() {
	for {
		select {
		case j := <-e.jobs:
			// Process the job synchronously — next job waits in the channel.
			info, err := e.doSnapshot(j.projectPath, j.message)
			// Send result to the caller. resultCh is buffered(1) so this never blocks.
			j.resultCh <- jobResult{info: info, err: err}

		case <-e.ctx.Done():
			log.Println("[Snapshot] Worker goroutine stopped cleanly.")
			return
		}
	}
}

// ── Core Snapshot Algorithm ────────────────────────────────────────────────────

// doSnapshot executes the full snapshot pipeline for one project directory.
// Always called by the worker goroutine — never directly.
func (e *Engine) doSnapshot(projectPath, message string) (SnapshotInfo, error) {
	log.Printf("[Snapshot] Starting snapshot of %q...", projectPath)

	// 1. Walk and hash all text files in the project directory.
	files, err := e.walkProject(projectPath)
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("walk %q: %w", projectPath, err)
	}
	if len(files) == 0 {
		return SnapshotInfo{}, fmt.Errorf("no snapshottable files found in %q", projectPath)
	}

	// 2. Compute the deterministic tree hash from all (path, contentHash) pairs.
	// This is the "commit hash" — identical project states produce identical hashes.
	treeHash := computeTreeHash(files)
	log.Printf("[Snapshot] Tree hash: %s... (%d files)", treeHash[:7], len(files))

	// 3. Idempotency check: if this exact state already exists, return it.
	// This prevents duplicate snapshots when the project hasn't changed.
	var existingID int64
	lookupErr := e.db.QueryRowContext(e.ctx,
		`SELECT id FROM snapshots WHERE project_path = ? AND commit_hash = ? LIMIT 1`,
		projectPath, treeHash,
	).Scan(&existingID)
	if lookupErr == nil {
		log.Printf("[Snapshot] Duplicate state detected — returning existing snapshot %d.", existingID)
		return e.loadSnapshotByID(existingID)
	}

	// 4. Persist all blobs and metadata in a single atomic transaction.
	return e.persistSnapshot(projectPath, treeHash, message, files)
}

// walkProject walks the project directory, collecting fileRecords for all text files.
//
// Python analogy:
//
//	records = []
//	for root, dirs, files in os.walk(projectPath):
//	    dirs[:] = [d for d in dirs if d not in ignoreDirs]  # prune subtrees in-place
//	    for fname in files:
//	        if os.path.splitext(fname)[1] in binaryExtensions: continue
//	        path = os.path.join(root, fname)
//	        hash_, content, lines = hash_file(path)
//	        records.append(FileRecord(relPath, hash_, content, lines))
//	records.sort(key=lambda r: r.relPath)
func (e *Engine) walkProject(root string) ([]fileRecord, error) {
	var records []fileRecord

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip inaccessible files/dirs rather than aborting the entire walk.
			// Python analogy: `except (PermissionError, OSError): continue`
			log.Printf("[Snapshot] Skipping inaccessible path %s: %v", path, err)
			return nil
		}

		if d.IsDir() {
			// filepath.SkipDir is a special sentinel error that tells WalkDir to
			// skip the ENTIRE subtree rooted at this directory — not just skip the
			// directory entry itself. This is the key mechanism for pruning
			// node_modules, .git, etc. without traversing millions of files.
			if _, skip := ignoreDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}

		// Fast-path binary check by extension — avoids opening the file.
		if _, isBin := binaryExtensions[strings.ToLower(filepath.Ext(path))]; isBin {
			return nil
		}

		// Compute the canonical relative path (always forward-slash, even on Windows).
		// Python analogy: os.path.relpath(path, root).replace(os.sep, '/')
		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		// Hash the file (reads content, detects binary, counts lines).
		hash, content, lineCount, hashErr := hashFile(path)
		if hashErr == errBinaryFile {
			return nil // silently skip binary content (null bytes detected)
		}
		if hashErr != nil {
			log.Printf("[Snapshot] Skipping %s: %v", relPath, hashErr)
			return nil
		}

		records = append(records, fileRecord{
			relPath:   relPath,
			hash:      hash,
			sizeBytes: int64(len(content)),
			lineCount: lineCount,
			content:   content,
		})
		return nil
	})

	if walkErr != nil {
		return nil, walkErr
	}

	// Sort by relPath for canonical, deterministic ordering before tree hash.
	// Python analogy: records.sort(key=lambda r: r.relPath)
	sort.Slice(records, func(i, j int) bool {
		return records[i].relPath < records[j].relPath
	})

	return records, nil
}

// hashFile reads a file and computes its SHA-1 hash, line count, and content.
//
// Python analogy:
//
//	with open(path, 'rb') as f:
//	    content = f.read()
//	if b'\x00' in content[:512]:                # binary detection
//	    raise BinaryFileError
//	digest = hashlib.sha1(content).hexdigest()  # SHA-1 hash
//	lines  = content.decode().count('\n') + 1   # line count
func hashFile(path string) (hash string, content []byte, lineCount int, err error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		return "", nil, 0, statErr
	}
	if info.Size() > maxFileSizeBytes {
		return "", nil, 0, fmt.Errorf("file exceeds %d byte limit (%d bytes)", maxFileSizeBytes, info.Size())
	}

	f, err := os.Open(path)
	if err != nil {
		return "", nil, 0, err
	}
	defer f.Close()

	// io.ReadAll reads the entire file into a []byte slice.
	// Python analogy: f.read() opened in binary mode ('rb').
	content, err = io.ReadAll(f)
	if err != nil {
		return "", nil, 0, err
	}

	if isBinaryContent(content) {
		return "", nil, 0, errBinaryFile
	}

	// sha1.New() creates a new incremental SHA-1 state machine (like hashlib.sha1()).
	// h.Write(data) feeds bytes in — equivalent to h.update(data) in Python.
	// h.Sum(nil) finalizes the digest, returning a 20-byte []byte array.
	// hex.EncodeToString converts those 20 bytes to the familiar 40-char hex string.
	//
	// Python analogy:
	//   import hashlib
	//   h = hashlib.sha1(content)        # one-shot
	//   hash = h.hexdigest()             # ≈ hex.EncodeToString(h.Sum(nil))
	h := sha1.New()
	h.Write(content)
	hash = hex.EncodeToString(h.Sum(nil))

	// strings.Count counts non-overlapping occurrences of "\n".
	// Adding 1 handles the last line (which typically has no trailing newline).
	// Python analogy: content.decode().count('\n') + 1
	lineCount = strings.Count(string(content), "\n") + 1

	return hash, content, lineCount, nil
}

// computeTreeHash derives the deterministic commit hash for a complete snapshot.
//
// Algorithm:
//  1. Files MUST already be sorted by relPath (done in walkProject).
//  2. Build canonical string: "%40s %s\n" per file (sha1hex + space + relpath).
//  3. SHA-1 hash the canonical string → the tree hash.
//
// Python analogy:
//
//	tree_str = "\n".join(f"{f.hash} {f.relPath}" for f in sorted_files) + "\n"
//	commit_hash = hashlib.sha1(tree_str.encode()).hexdigest()
//
// This mirrors Git's tree object hashing (simplified). Two identical project
// states ALWAYS produce the same commit hash. One changed byte → different hash.
func computeTreeHash(files []fileRecord) string {
	var sb strings.Builder
	// sb.WriteString is like Python's io.StringIO for efficient string building.
	// Python analogy: ''.join(f"{f.hash} {f.relPath}\n" for f in files)
	for _, f := range files {
		sb.WriteString(f.hash)
		sb.WriteByte(' ')
		sb.WriteString(f.relPath)
		sb.WriteByte('\n')
	}

	// sha1.Sum() is a one-shot convenience that avoids New()+Write()+Sum(nil).
	// It returns [20]byte (a FIXED-SIZE ARRAY, not a slice).
	// We use treeHash[:] to convert it to a []byte slice for hex.EncodeToString.
	//
	// Go arrays vs slices analogy for Python devs:
	//   [20]byte is like a tuple of 20 ints — fixed size, value type.
	//   []byte  is like a Python list — dynamic size, reference type.
	//   treeHash[:] creates a slice VIEW over the array (no data copy).
	digest := sha1.Sum([]byte(sb.String()))
	return hex.EncodeToString(digest[:])
}

// ── Database Operations ────────────────────────────────────────────────────────

// persistSnapshot writes all snapshot data to the database in one atomic transaction.
// Content-addressable: INSERT OR IGNORE on snapshot_blobs deduplicates file content.
func (e *Engine) persistSnapshot(projectPath, treeHash, message string, files []fileRecord) (SnapshotInfo, error) {
	// BeginTx starts a transaction. All subsequent DB operations are atomic:
	// either ALL succeed (on Commit) or NONE apply (on Rollback or error).
	// Python analogy: `with db.transaction():` in Django ORM.
	tx, err := e.db.BeginTx(e.ctx, nil)
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() // no-op if Commit() is reached; ensures cleanup on early return

	// Insert the snapshot header record.
	res, err := tx.ExecContext(e.ctx,
		`INSERT INTO snapshots (project_path, commit_hash, message) VALUES (?, ?, ?)`,
		projectPath, treeHash, message,
	)
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("insert snapshot header: %w", err)
	}
	// LastInsertId returns the auto-increment primary key of the new row.
	// Python analogy: cursor.lastrowid after cursor.execute(INSERT ...)
	snapshotID, _ := res.LastInsertId()

	var totalBytes int64
	for _, f := range files {
		// INSERT OR IGNORE: if a blob with this hash already exists, skip silently.
		// This is the core content-addressable deduplication mechanism.
		// Python analogy: db.get_or_create(SnapshotBlob, hash=f.hash, defaults={"content": f.content})
		_, err = tx.ExecContext(e.ctx,
			`INSERT OR IGNORE INTO snapshot_blobs (hash, content) VALUES (?, ?)`,
			f.hash, f.content,
		)
		if err != nil {
			return SnapshotInfo{}, fmt.Errorf("insert blob for %s: %w", f.relPath, err)
		}

		// Insert the file's entry in this snapshot's tree index.
		_, err = tx.ExecContext(e.ctx,
			`INSERT INTO snapshot_files (snapshot_id, rel_path, file_hash, size_bytes, line_count)
			 VALUES (?, ?, ?, ?, ?)`,
			snapshotID, f.relPath, f.hash, f.sizeBytes, f.lineCount,
		)
		if err != nil {
			return SnapshotInfo{}, fmt.Errorf("insert file record for %s: %w", f.relPath, err)
		}
		totalBytes += f.sizeBytes
	}

	if err := tx.Commit(); err != nil {
		return SnapshotInfo{}, fmt.Errorf("commit transaction: %w", err)
	}

	info := SnapshotInfo{
		ID:          snapshotID,
		ProjectPath: projectPath,
		FullHash:    treeHash,
		CommitHash:  treeHash[:7],
		Message:     message,
		FileCount:   len(files),
		TotalBytes:  totalBytes,
	}

	log.Printf("[Snapshot] ✓ Created %s — %d files, %s",
		info.CommitHash, info.FileCount, formatBytes(info.TotalBytes))

	// Emit the event to the JavaScript frontend.
	// JS listener: window.runtime.EventsOn("snapshot:created", (info) => {...})
	runtime.EventsEmit(e.ctx, "snapshot:created", info)

	return info, nil
}

// loadSnapshotFiles retrieves the file tree of a snapshot from the DB.
// Returns map[relPath]snapshotFileRow for O(1) lookup during diff.
func (e *Engine) loadSnapshotFiles(snapshotID int64) (map[string]snapshotFileRow, error) {
	rows, err := e.db.QueryContext(e.ctx,
		`SELECT rel_path, file_hash, line_count FROM snapshot_files WHERE snapshot_id = ?`,
		snapshotID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	files := make(map[string]snapshotFileRow)
	for rows.Next() {
		var row snapshotFileRow
		if err := rows.Scan(&row.relPath, &row.fileHash, &row.lineCount); err != nil {
			return nil, err
		}
		files[row.relPath] = row
	}
	return files, rows.Err()
}

// loadBlob retrieves a file's raw content from the content-addressable store.
func (e *Engine) loadBlob(hash string) ([]byte, error) {
	var content []byte
	err := e.db.QueryRowContext(e.ctx,
		`SELECT content FROM snapshot_blobs WHERE hash = ?`, hash,
	).Scan(&content)
	return content, err
}

// loadSnapshotByID is a convenience loader used for duplicate detection in doSnapshot.
func (e *Engine) loadSnapshotByID(id int64) (SnapshotInfo, error) {
	var info SnapshotInfo
	var fullHash string
	err := e.db.QueryRowContext(e.ctx, `
		SELECT s.id, s.project_path, s.commit_hash, COALESCE(s.message,''), s.created_at,
		       COUNT(sf.id), COALESCE(SUM(sf.size_bytes), 0)
		FROM snapshots s
		LEFT JOIN snapshot_files sf ON sf.snapshot_id = s.id
		WHERE s.id = ?
		GROUP BY s.id
	`, id).Scan(&info.ID, &info.ProjectPath, &fullHash,
		&info.Message, &info.CreatedAt, &info.FileCount, &info.TotalBytes)
	if err != nil {
		return SnapshotInfo{}, err
	}
	info.FullHash = fullHash
	if len(fullHash) >= 7 {
		info.CommitHash = fullHash[:7]
	}
	return info, nil
}

// diffBlobs loads two file blobs and returns the LCS-based line diff counts.
func (e *Engine) diffBlobs(oldHash, newHash string, totalLines int) (added, deleted int) {
	oldContent, err := e.loadBlob(oldHash)
	if err != nil {
		return 0, 0 // can't load blob — treat file as unchanged
	}
	newContent, err := e.loadBlob(newHash)
	if err != nil {
		return 0, 0
	}
	return diffLineCount(oldContent, newContent, totalLines)
}

// ── Line-Level Diff Algorithm ──────────────────────────────────────────────────

// diffLineCount computes the count of added and deleted lines between two file contents.
//
// Algorithm: Longest Common Subsequence (LCS)
//
//	linesAdded   = len(newLines) - LCS(oldLines, newLines)
//	linesDeleted = len(oldLines) - LCS(oldLines, newLines)
//
// For files with totalLines > maxLCSLines, we use a fast approximation to
// avoid O(m×n) computation on very large files.
//
// Python analogy using difflib:
//
//	import difflib
//	matcher = difflib.SequenceMatcher(None, old_lines, new_lines)
//	lcs_len = sum(b.size for b in matcher.get_matching_blocks())
//	added   = len(new_lines) - lcs_len
//	deleted = len(old_lines) - lcs_len
func diffLineCount(oldContent, newContent []byte, totalLines int) (added, deleted int) {
	oldLines := strings.Split(string(oldContent), "\n")
	newLines := strings.Split(string(newContent), "\n")

	if totalLines > maxLCSLines {
		// Approximation for large files: show net line count change.
		// This avoids O(m×n) for files with thousands of lines.
		delta := len(newLines) - len(oldLines)
		if delta > 0 {
			return delta, 0
		}
		return 0, -delta
	}

	lcsLen := lcsLength(oldLines, newLines)
	added = len(newLines) - lcsLen
	deleted = len(oldLines) - lcsLen
	if added < 0 {
		added = 0
	}
	if deleted < 0 {
		deleted = 0
	}
	return
}

// lcsLength computes the length of the Longest Common Subsequence of two string slices.
//
// Uses a space-optimized rolling-row DP table: O(min(m,n)) space, O(m×n) time.
// We process 'a' row by row, keeping only the previous and current DP rows.
//
// Python analogy (recursive with memoization):
//
//	from functools import lru_cache
//	@lru_cache(maxsize=None)
//	def lcs(i, j):
//	    if i == 0 or j == 0: return 0
//	    if a[i-1] == b[j-1]: return lcs(i-1, j-1) + 1
//	    return max(lcs(i-1, j), lcs(i, j-1))
//
// Go uses the iterative bottom-up approach instead (no recursion overhead).
func lcsLength(a, b []string) int {
	// Optimization: ensure b is the shorter slice to minimize memory allocation.
	// LCS(a, b) == LCS(b, a) so swapping is always safe.
	if len(a) < len(b) {
		a, b = b, a
	}

	// prev[j] = LCS length for a[:i-1] and b[:j]
	// curr[j] = LCS length for a[:i]   and b[:j]
	// We only need these two rows — the full m×n matrix is unnecessary.
	prev := make([]int, len(b)+1)

	for _, lineA := range a {
		curr := make([]int, len(b)+1)
		for j, lineB := range b {
			if lineA == lineB {
				// Lines match: extend the LCS from the diagonal (prev[j] = a[:i-1], b[:j])
				curr[j+1] = prev[j] + 1
			} else if prev[j+1] > curr[j] {
				// Best LCS without lineA (from above in the table)
				curr[j+1] = prev[j+1]
			} else {
				// Best LCS without lineB (from the left in the current row)
				curr[j+1] = curr[j]
			}
		}
		prev = curr // advance the rolling window
	}

	return prev[len(b)]
}

// ── Helper Functions ───────────────────────────────────────────────────────────

// isBinaryContent returns true if data appears to be binary by checking for null bytes.
// Null bytes (0x00) almost never appear in text files but are common in binary formats.
// Checking only the first 512 bytes is sufficient and fast.
//
// Python analogy: b'\x00' in data[:512]
func isBinaryContent(data []byte) bool {
	n := 512
	if len(data) < n {
		n = len(data)
	}
	for _, b := range data[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

// formatBytes converts a byte count to a human-readable string (KB, MB).
func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/1024/1024)
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// ── Snapshot Management (Restore, Update, Delete) ──────────────────────────────

// UpdateSnapshot updates the message of an existing snapshot.
func (e *Engine) UpdateSnapshot(id int64, message string) error {
	_, err := e.db.Exec(`UPDATE snapshots SET message = ? WHERE id = ?`, message, id)
	return err
}

// DeleteSnapshot deletes a snapshot and cleans up orphaned blobs.
func (e *Engine) DeleteSnapshot(id int64) error {
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Delete the snapshot files mappings
	if _, err := tx.Exec(`DELETE FROM snapshot_files WHERE snapshot_id = ?`, id); err != nil {
		return err
	}

	// 2. Delete the snapshot metadata
	if _, err := tx.Exec(`DELETE FROM snapshots WHERE id = ?`, id); err != nil {
		return err
	}

	// 3. Cleanup orphaned blobs (hashes that no longer exist in any snapshot)
	if _, err := tx.Exec(`
		DELETE FROM snapshot_blobs 
		WHERE hash NOT IN (SELECT DISTINCT file_hash FROM snapshot_files)
	`); err != nil {
		return err
	}

	return tx.Commit()
}

// RestoreSnapshot restores a project directory to the exact state of a snapshot.
// It uses an in-place replacement mechanism to avoid OS file locking issues.
func (e *Engine) RestoreSnapshot(id int64, projectPath string) error {
	// 1. Fetch all files for this snapshot
	rows, err := e.db.Query(`
		SELECT sf.rel_path, sb.content 
		FROM snapshot_files sf
		JOIN snapshot_blobs sb ON sf.file_hash = sb.hash
		WHERE sf.snapshot_id = ?
	`, id)
	if err != nil {
		return fmt.Errorf("failed to fetch snapshot files: %w", err)
	}
	defer rows.Close()

	type fileData struct {
		relPath string
		content []byte
	}
	var files []fileData
	snapshotFiles := make(map[string]bool)

	for rows.Next() {
		var fd fileData
		if err := rows.Scan(&fd.relPath, &fd.content); err != nil {
			return fmt.Errorf("error scanning file data: %w", err)
		}
		files = append(files, fd)
		snapshotFiles[fd.relPath] = true
	}

	// 2. Delete files that are NOT in the snapshot, keeping ignored/system files.
	filepath.WalkDir(projectPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == projectPath {
			return nil
		}
		if d.IsDir() {
			if _, skip := ignoreDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, relErr := filepath.Rel(projectPath, path)
		if relErr != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		// Don't delete database files or running executables
		lowerName := strings.ToLower(d.Name())
		if lowerName == "sentinel.db" || lowerName == "sentinel.db-wal" || lowerName == "sentinel.db-shm" {
			return nil
		}
		if strings.HasSuffix(lowerName, ".exe") {
			return nil
		}
		// Also don't delete binary files since we never track them in snapshots
		if _, isBin := binaryExtensions[strings.ToLower(filepath.Ext(path))]; isBin {
			return nil
		}

		// If the file is tracked but not in the snapshot we are restoring to, delete it.
		if !snapshotFiles[relPath] {
			os.Remove(path)
		}
		return nil
	})

	// 3. Write all files from the snapshot to disk
	for _, f := range files {
		fullPath := filepath.Join(projectPath, f.relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", f.relPath, err)
		}
		if err := os.WriteFile(fullPath, f.content, 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", f.relPath, err)
		}
	}

	return nil
}
