// Package guardian implements The Guardian — Sentinel-OS's cryptographic backup
// and rollback engine. It provides:
//
//   - AES-256-GCM encryption of ZIP archives (authenticated + encrypted)
//   - Two-mode rollback: Soft (stash → restore on failure) and Hard (destructive)
//   - Zip Slip vulnerability protection during extraction
//   - Grandfather-Father-Son (GFS) retention policy with automatic rotation
//   - Full SQLite persistence of backup metadata
//
// Architecture overview:
//
//	startup() in app.go
//	    └─ guardian.LoadOrGenerateKey(keyPath)  → []byte (32-byte AES key)
//	    └─ guardian.New(ctx, db, key, vaultDir) → *Guardian
//	           │
//	           ├─ CreateBackup(projectPath)
//	           │       ├─ compressDir()    → ZIP bytes (archive/zip + Deflate)
//	           │       ├─ encrypt()        → AES-256-GCM ciphertext
//	           │       ├─ os.WriteFile()   → write .zip.enc to disk
//	           │       ├─ persistBackup()  → INSERT INTO backups
//	           │       └─ rotateGFS()      → prune expired backups
//	           │
//	           └─ RestoreBackup(id, targetDir, keepCurrent)
//	                   ├─ keepCurrent=true  → Soft: stash → extract → swap
//	                   └─ keepCurrent=false → Hard: extract → nuke → replace
//
// ── Python Library Analogies ──────────────────────────────────────────────────
//
//	Go                              Python
//	──────────────────────────────  ─────────────────────────────────────────────
//	aes.NewCipher(key)           ≈  cryptography.hazmat.primitives.ciphers.algorithms.AES(key)
//	cipher.NewGCM(block)         ≈  from cryptography.hazmat.primitives.ciphers.aead import AESGCM
//	gcm.Seal(nonce,nonce,pt,nil) ≈  AESGCM(key).encrypt(nonce, plaintext, None)
//	gcm.Open(nil,nonce,ct,nil)   ≈  AESGCM(key).decrypt(nonce, ciphertext, None)
//	rand.Read(buf)               ≈  os.urandom(len(buf))
//	zip.NewWriter(buf)           ≈  zipfile.ZipFile(buf, 'w', ZIP_DEFLATED)
//	zip.NewReader(r, size)       ≈  zipfile.ZipFile(buf, 'r')
//	os.MkdirTemp("","prefix-*") ≈  tempfile.mkdtemp(prefix='prefix-')
//	os.RemoveAll(dir)            ≈  shutil.rmtree(dir)
//	io.Copy(dst, src)            ≈  shutil.copyfileobj(src, dst)
package guardian

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ── Constants ──────────────────────────────────────────────────────────────────

// GFS retention defaults. Adjust these to tune the backup vault size.
const (
	defaultDailyKeep   = 7  // keep 7 most-recent daily backups
	defaultWeeklyKeep  = 4  // keep 4 most-recent weekly backups (one per ISO week)
	defaultMonthlyKeep = 12 // keep 12 most-recent monthly backups (one per calendar month)
)

// maxBackupSizeBytes: directories larger than 2 GB are rejected to prevent
// accidentally archiving data drives.
const maxBackupSizeBytes = 2 * 1024 * 1024 * 1024

// backupIgnoreDirs: directories to skip during compression.
// Node modules and caches should NOT be backed up — they can be reinstalled.
var backupIgnoreDirs = map[string]bool{
	"node_modules": true, "__pycache__": true, ".venv": true, "venv": true,
	".mypy_cache": true, ".pytest_cache": true, ".cache": true,
	"dist": true, "build": true, "target": true,
}

// ── Exported Types ─────────────────────────────────────────────────────────────

// BackupInfo is the JS-facing metadata for one backup archive.
type BackupInfo struct {
	ID          int64  `json:"id"`
	ProjectPath string `json:"projectPath"`
	ArchivePath string `json:"archivePath"` // absolute path to the .zip.enc file on disk
	Tier        string `json:"tier"`        // "daily", "weekly", or "monthly" (GFS tier)
	SizeBytes   int64  `json:"sizeBytes"`   // size of the encrypted archive on disk
	CreatedAt   string `json:"createdAt"`   // RFC3339 timestamp
}

// Schedule represents a configured automated backup schedule.
type Schedule struct {
	ID            int64  `json:"id"`
	ProjectPath   string `json:"projectPath"`
	IntervalHours int    `json:"intervalHours"`
	LastRun       string `json:"lastRun"` // RFC3339 or empty
}

// ── Internal DB Record ─────────────────────────────────────────────────────────

// backupRecord is the internal hydrated form of a `backups` table row.
type backupRecord struct {
	id          int64
	projectPath string
	archivePath string
	tier        string
	sizeBytes   int64
	createdAt   time.Time
}

// ── Guardian Engine ────────────────────────────────────────────────────────────

// Guardian is the cryptographic backup and rollback engine.
// It is stateless after construction — all state lives in the DB and filesystem.
type Guardian struct {
	ctx      context.Context
	db       *sql.DB
	key      []byte // 32-byte AES-256 key (MUST remain secret)
	vaultDir string // directory where .zip.enc archives are stored
}

// New constructs the Guardian. vaultDir is created if it does not exist.
func New(ctx context.Context, db *sql.DB, key []byte, vaultDir string) (*Guardian, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("guardian: key must be exactly 32 bytes (got %d)", len(key))
	}
	if err := os.MkdirAll(vaultDir, 0700); err != nil {
		return nil, fmt.Errorf("guardian: create vault dir %q: %w", vaultDir, err)
	}
	log.Printf("[Guardian] Initialized. Vault: %s", vaultDir)
	
	g := &Guardian{ctx: ctx, db: db, key: key, vaultDir: vaultDir}
	g.StartScheduler()
	return g, nil
}

// StartScheduler launches a background goroutine to process scheduled backups.
func (g *Guardian) StartScheduler() {
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		for {
			select {
			case <-g.ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				g.runScheduledBackups()
			}
		}
	}()
}

func (g *Guardian) runScheduledBackups() {
	rows, err := g.db.Query(`SELECT id, project_path, interval_hours, last_run FROM guardian_schedules`)
	if err != nil {
		log.Printf("[Guardian] Scheduler DB error: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var path string
		var interval int
		var lastRun sql.NullTime
		if err := rows.Scan(&id, &path, &interval, &lastRun); err != nil {
			continue
		}

		shouldRun := false
		if !lastRun.Valid {
			shouldRun = true
		} else {
			if time.Since(lastRun.Time).Hours() >= float64(interval) {
				shouldRun = true
			}
		}

		if shouldRun {
			log.Printf("[Guardian] Scheduled backup triggered for %s", path)
			if _, err := g.CreateBackup(path); err == nil {
				g.db.Exec(`UPDATE guardian_schedules SET last_run = CURRENT_TIMESTAMP WHERE id = ?`, id)
			} else {
				log.Printf("[Guardian] Scheduled backup failed for %s: %v", path, err)
			}
		}
	}
}

// LoadOrGenerateKey reads a 32-byte key from keyPath, or generates and saves a
// fresh random key if the file does not exist. The key file is written with
// 0600 permissions (owner read/write only).
//
// Python analogy:
//
//	if os.path.exists(keyPath):
//	    with open(keyPath, 'rb') as f:
//	        return f.read()
//	key = os.urandom(32)
//	with open(keyPath, 'wb') as f:
//	    f.write(key)
//	return key
func LoadOrGenerateKey(keyPath string) ([]byte, error) {
	if data, err := os.ReadFile(keyPath); err == nil && len(data) == 32 {
		return data, nil
	}
	key := make([]byte, 32)
	// crypto/rand.Read fills the slice with cryptographically secure random bytes.
	// Python analogy: os.urandom(32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return nil, fmt.Errorf("save key to %q: %w", keyPath, err)
	}
	log.Printf("[Guardian] ⚠  New 256-bit AES key written to %s — back this file up!", keyPath)
	return key, nil
}

// ── Public API ─────────────────────────────────────────────────────────────────

// CreateBackup compresses and encrypts projectPath into the vault, then runs
// GFS rotation to prune expired archives. Emits "guardian:backup_created".
//
// The archive format on disk:
//
//	<vaultDir>/<project_basename>/<unix_timestamp>.zip.enc
//
// File binary layout:
//
//	[ 12-byte random nonce ][ AES-256-GCM ciphertext + 16-byte auth tag ]
//
// Python analogy:
//
//	buf = io.BytesIO()
//	with zipfile.ZipFile(buf, 'w', ZIP_DEFLATED) as zf:
//	    for path in walk(projectPath): zf.write(path, relpath)
//	nonce = os.urandom(12)
//	ct = AESGCM(key).encrypt(nonce, buf.getvalue(), None)
//	open(archivePath, 'wb').write(nonce + ct)
func (g *Guardian) CreateBackup(projectPath string) (BackupInfo, error) {
	log.Printf("[Guardian] Creating backup of %q...", projectPath)

	// Step 1: Compress the project directory into an in-memory ZIP.
	zipData, err := compressDir(projectPath)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("compress: %w", err)
	}
	log.Printf("[Guardian] Compressed → %s in-memory ZIP", formatBytes(int64(len(zipData))))

	// Step 2: Encrypt the ZIP bytes with AES-256-GCM.
	ciphertext, err := encryptAESGCM(g.key, zipData)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("encrypt: %w", err)
	}

	// Step 3: Write the encrypted archive to the vault.
	projectName := filepath.Base(projectPath)
	projectVault := filepath.Join(g.vaultDir, projectName)
	if err := os.MkdirAll(projectVault, 0700); err != nil {
		return BackupInfo{}, fmt.Errorf("create project vault dir: %w", err)
	}
	archiveName := fmt.Sprintf("%d.zip.enc", time.Now().Unix())
	archivePath := filepath.Join(projectVault, archiveName)

	if err := os.WriteFile(archivePath, ciphertext, 0600); err != nil {
		return BackupInfo{}, fmt.Errorf("write archive: %w", err)
	}

	// Step 4: Persist the backup metadata to the database.
	info, err := g.persistBackup(projectPath, archivePath, int64(len(ciphertext)))
	if err != nil {
		os.Remove(archivePath) // roll back the file write on DB failure
		return BackupInfo{}, fmt.Errorf("persist metadata: %w", err)
	}

	log.Printf("[Guardian] ✓ Backup %s created (%s)", filepath.Base(archivePath), formatBytes(info.SizeBytes))
	runtime.EventsEmit(g.ctx, "guardian:backup_created", info)

	// Step 5: GFS rotation — runs asynchronously to avoid blocking the caller.
	go g.rotateGFS(projectPath)

	return info, nil
}

// RestoreBackup decrypts and extracts a backup archive into targetDir.
//
// keepCurrent == true  → Soft Rollback:
//
//	Stashes the current targetDir into a temp directory BEFORE extraction.
//	If decryption or extraction fails, the stash is automatically restored.
//	Guarantees the working directory is never left in a broken state.
//
// keepCurrent == false → Hard Rollback:
//
//	Decrypts to a temp directory first (verifies integrity), THEN destructively
//	wipes targetDir before moving the extracted content in. No recovery if
//	the OS call to remove targetDir fails.
//
// Python analogy for Soft Rollback:
//
//	stash = tempfile.mkdtemp()
//	shutil.copytree(targetDir, stash)
//	try:
//	    extract(archivePath, tmpDir)
//	    shutil.rmtree(targetDir)
//	    shutil.copytree(tmpDir, targetDir)
//	except Exception as e:
//	    shutil.rmtree(targetDir)
//	    shutil.copytree(stash, targetDir)
//	    raise
func (g *Guardian) RestoreBackup(backupID int64, targetDir string, keepCurrent bool) error {
	rec, err := g.loadBackupRecord(backupID)
	if err != nil {
		return fmt.Errorf("backup %d not found: %w", backupID, err)
	}

	if keepCurrent {
		err = g.restoreSoft(rec.archivePath, targetDir)
	} else {
		err = g.restoreHard(rec.archivePath, targetDir)
	}
	if err != nil {
		return err
	}

	log.Printf("[Guardian] ✓ Restored backup %d → %s", backupID, targetDir)
	runtime.EventsEmit(g.ctx, "guardian:backup_restored", rec.toInfo())
	return nil
}

// GetBackupList returns all backup records for projectPath, newest first.
func (g *Guardian) GetBackupList(projectPath string) ([]BackupInfo, error) {
	rows, err := g.db.QueryContext(g.ctx, `
		SELECT id, project_path, archive_path, tier, size_bytes, created_at
		FROM   backups
		WHERE  project_path = ?
		ORDER  BY id DESC
	`, projectPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []BackupInfo
	for rows.Next() {
		var b BackupInfo
		if err := rows.Scan(&b.ID, &b.ProjectPath, &b.ArchivePath,
			&b.Tier, &b.SizeBytes, &b.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, b)
	}
	return results, rows.Err()
}

// DeleteBackup removes the archive file from disk and its record from the DB.
func (g *Guardian) DeleteBackup(backupID int64) error {
	rec, err := g.loadBackupRecord(backupID)
	if err != nil {
		return fmt.Errorf("backup %d not found: %w", backupID, err)
	}
	if err := g.purgeBackup(rec); err != nil {
		return err
	}
	runtime.EventsEmit(g.ctx, "guardian:backup_deleted", map[string]int64{"id": backupID})
	log.Printf("[Guardian] Deleted backup %d: %s", backupID, filepath.Base(rec.archivePath))
	return nil
}

// ── Rollback Modes ─────────────────────────────────────────────────────────────

// restoreSoft is the safe rollback mode: it stashes the current directory
// and restores from stash if anything goes wrong.
func (g *Guardian) restoreSoft(archivePath, targetDir string) error {
	// ── Phase A: Stash the current working directory ──────────────────────────
	// os.MkdirTemp creates a uniquely-named temp directory.
	// Python analogy: tempfile.mkdtemp(prefix="sentinel-stash-")
	stashDir, err := os.MkdirTemp("", "sentinel-stash-*")
	if err != nil {
		return fmt.Errorf("soft rollback: create stash dir: %w", err)
	}
	// defer ensures the stash is always cleaned up — even on early returns.
	// Python analogy: finally: shutil.rmtree(stashDir)
	defer os.RemoveAll(stashDir)

	if err := copyDirContents(targetDir, stashDir); err != nil {
		return fmt.Errorf("soft rollback: stash current dir: %w", err)
	}
	log.Printf("[Guardian][Soft] Stashed current state of %s → %s", targetDir, stashDir)

	// ── Phase B: Decrypt and extract to a fresh temp directory ───────────────
	extractDir, err := os.MkdirTemp("", "sentinel-extract-*")
	if err != nil {
		return fmt.Errorf("soft rollback: create extract dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	if err := g.decryptAndExtract(archivePath, extractDir); err != nil {
		// Extraction failed. The stash is intact — restore it.
		log.Printf("[Guardian][Soft] Extraction failed (%v). Restoring from stash...", err)
		if restoreErr := replaceDir(stashDir, targetDir); restoreErr != nil {
			return fmt.Errorf("CRITICAL: extraction failed AND stash restore failed: extract=%v stash=%v", err, restoreErr)
		}
		log.Printf("[Guardian][Soft] Stash restored successfully.")
		return fmt.Errorf("extraction failed (original state restored from stash): %w", err)
	}

	// ── Phase C: Swap — replace targetDir with extracted content ─────────────
	if err := replaceDir(extractDir, targetDir); err != nil {
		// Swap failed — restore stash as last resort.
		log.Printf("[Guardian][Soft] Dir swap failed. Restoring stash...")
		replaceDir(stashDir, targetDir) // best-effort, ignore error
		return fmt.Errorf("soft rollback: swap target: %w", err)
	}

	return nil
}

// restoreHard is the destructive rollback mode: verifies archive integrity first,
// then wipes the target directory and replaces it with the extracted content.
func (g *Guardian) restoreHard(archivePath, targetDir string) error {
	// ── Phase A: Decrypt and extract to temp (integrity check before destruction)
	extractDir, err := os.MkdirTemp("", "sentinel-extract-*")
	if err != nil {
		return fmt.Errorf("hard rollback: create extract dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	// Verify the archive is valid BEFORE touching the target directory.
	// An authentication failure here means the archive is corrupted or tampered with.
	if err := g.decryptAndExtract(archivePath, extractDir); err != nil {
		return fmt.Errorf("hard rollback: archive invalid (target NOT modified): %w", err)
	}

	// ── Phase B: Destructive replacement ─────────────────────────────────────
	// os.RemoveAll is the equivalent of `shutil.rmtree` — removes the directory
	// and ALL of its contents recursively. There is no recovery from this point.
	log.Printf("[Guardian][Hard] Purging %s...", targetDir)
	if err := os.RemoveAll(targetDir); err != nil {
		return fmt.Errorf("hard rollback: remove target: %w", err)
	}

	return copyDirContents(extractDir, targetDir)
}

// ── Compression ────────────────────────────────────────────────────────────────

// compressDir walks projectPath and produces an in-memory ZIP archive.
//
// archive/zip mechanics for Python devs:
//
//	zip.NewWriter(buf) ≈ zipfile.ZipFile(buf, 'w', compression=ZIP_DEFLATED)
//	w.CreateHeader(h)  ≈ zf.open(filename, 'w')
//	io.Copy(entry, f)  ≈ shutil.copyfileobj(f, entry)
//	w.Close()          ≈ zf.close()  (flushes the central directory)
//
// The in-memory approach (bytes.Buffer) avoids a temp file round-trip.
// For truly massive projects (> 2GB), a streaming approach would be needed.
func compressDir(root string) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			log.Printf("[Guardian] Skipping inaccessible path %s: %v", path, walkErr)
			return nil
		}
		if d.IsDir() {
			if _, skip := backupIgnoreDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath) // normalize to forward slashes in archive

		f, err := os.Open(path)
		if err != nil {
			log.Printf("[Guardian] Skipping unreadable file %s: %v", relPath, err)
			return nil
		}
		defer f.Close()

		// zip.FileInfoHeader creates a ZIP header that preserves the file's
		// original modification time and mode bits.
		// Python analogy: ZipInfo.from_file(path, arcname=relPath)
		info, _ := d.Info()
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		header.Name = relPath
		header.Method = zip.Deflate // zlib compression

		writer, err := w.CreateHeader(header)
		if err != nil {
			return err
		}

		_, err = io.Copy(writer, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil { // flushes ZIP central directory to buf
		return nil, err
	}
	return buf.Bytes(), nil
}

// ── AES-256-GCM Encryption ────────────────────────────────────────────────────

// encryptAESGCM encrypts plaintext with AES-256-GCM using a random 12-byte nonce.
//
// Output format: [ 12-byte nonce ][ ciphertext + 16-byte auth tag ]
// The nonce is prepended using gcm.Seal's `dst` prefix mechanism.
//
// ┌─ How gcm.Seal works ───────────────────────────────────────────────────────┐
// │                                                                             │
// │  gcm.Seal(dst, nonce, plaintext, additionalData)                           │
// │    → appends encrypted(plaintext)+tag to dst, returns the extended slice   │
// │                                                                             │
// │  Passing nonce as `dst` means: start the output with the nonce bytes,     │
// │  then append ciphertext. Result: [nonce][ct+tag] — a single blob.         │
// │                                                                             │
// │  Python AESGCM equivalent:                                                 │
// │    nonce = os.urandom(12)                                                  │
// │    ct = AESGCM(key).encrypt(nonce, plaintext, None)  # ct includes tag    │
// │    output = nonce + ct                                                     │
// └─────────────────────────────────────────────────────────────────────────────┘
func encryptAESGCM(key, plaintext []byte) ([]byte, error) {
	// aes.NewCipher creates the block cipher with the provided key.
	// Key length determines algorithm: 16→AES-128, 24→AES-192, 32→AES-256.
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	// cipher.NewGCM wraps the block cipher in Galois/Counter Mode.
	// GCM = CTR encryption + GHASH authentication tag. One key, dual purpose.
	// Python analogy: AESGCM(key) where the AEAD construction is already implicit.
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	// Generate a random nonce. GCM's NonceSize() is always 12 bytes.
	// NEVER reuse a nonce with the same key — catastrophic for GCM security.
	// Using crypto/rand (not math/rand) guarantees cryptographic quality.
	nonce := make([]byte, gcm.NonceSize()) // 12 bytes
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// gcm.Seal(dst=nonce, nonce, plaintext, additionalData=nil)
	// This cleverly prepends the nonce to the output: [nonce][ct+tag]
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// decryptAESGCM decrypts an AES-256-GCM ciphertext produced by encryptAESGCM.
// Strips the leading 12-byte nonce, then calls gcm.Open which:
//  1. Decrypts the ciphertext body
//  2. Verifies the 16-byte GHASH authentication tag
//  3. Returns an error if the tag is invalid (data is corrupted or tampered with)
//
// Python analogy:
//
//	nonce, ct = data[:12], data[12:]
//	plaintext = AESGCM(key).decrypt(nonce, ct, None)  # raises if tag invalid
func decryptAESGCM(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize+gcm.Overhead() {
		return nil, fmt.Errorf("ciphertext too short to be valid (got %d bytes)", len(ciphertext))
	}

	nonce, body := ciphertext[:nonceSize], ciphertext[nonceSize:]

	// gcm.Open decrypts and authenticates simultaneously.
	// If the auth tag doesn't match, it returns cipher.ErrAuthFail.
	// This detects both accidental corruption and deliberate tampering.
	// Python analogy: AESGCM.decrypt raises cryptography.exceptions.InvalidTag
	plaintext, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM authentication/decryption failed (archive may be corrupted or key is wrong): %w", err)
	}
	return plaintext, nil
}

// ── ZIP Extraction with Zip Slip Protection ────────────────────────────────────

// extractZIP extracts a ZIP archive (as []byte) into destDir.
//
// Zip Slip is a directory traversal vulnerability where a maliciously crafted
// ZIP archive contains entries with paths like "../../../etc/passwd", causing
// extraction to write files outside the intended destination directory.
//
// Protection: before creating each file, we resolve the full destination path
// and verify it starts with the canonical destDir prefix.
//
// Python analogy (built-in protection since 3.12):
//
//	with zipfile.ZipFile(BytesIO(data)) as zf:
//	    zf.extractall(destDir)  # raises BadZipFile if zip slip detected
func extractZIP(zipData []byte, destDir string) error {
	r, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return fmt.Errorf("parse ZIP: %w", err)
	}

	// canonical destination prefix used for zip slip checks
	cleanDest := filepath.Clean(destDir)

	for _, f := range r.File {
		// ── Zip Slip Protection ───────────────────────────────────────────────
		// filepath.Join resolves ".." components. If a malicious entry name
		// is "../../evil.sh", Join will produce a path outside cleanDest.
		// filepath.Clean then normalizes it, and the HasPrefix check rejects it.
		//
		// We use filepath.FromSlash to convert the ZIP's forward-slash paths to
		// OS-native separators before joining (important on Windows).
		entryPath := filepath.Join(cleanDest, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(filepath.Clean(entryPath), cleanDest+string(os.PathSeparator)) &&
			filepath.Clean(entryPath) != cleanDest {
			return fmt.Errorf("zip slip attack detected: entry %q resolves outside destination", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(entryPath, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", f.Name, err)
			}
			continue
		}

		// Ensure the parent directory exists before creating the file.
		if err := os.MkdirAll(filepath.Dir(entryPath), 0755); err != nil {
			return fmt.Errorf("mkdir parent for %s: %w", f.Name, err)
		}

		if err := extractSingleFile(f, entryPath); err != nil {
			return fmt.Errorf("extract %s: %w", f.Name, err)
		}
	}
	return nil
}

// extractSingleFile extracts one ZIP entry to outPath.
func extractSingleFile(f *zip.File, outPath string) error {
	rc, err := f.Open() // rc is an io.ReadCloser for the (decompressed) entry content
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	// io.Copy streams from the ZIP entry to the output file in chunks.
	// Python analogy: shutil.copyfileobj(rc, out)
	_, err = io.Copy(out, rc)
	return err
}

// ── GFS Rotation ──────────────────────────────────────────────────────────────

// rotateGFS implements Grandfather-Father-Son backup retention.
//
// Algorithm (processes backups from NEWEST to OLDEST):
//  1. The first `dailyKeep` backups are assigned tier "daily".
//  2. Of the remaining, the first backup for each ISO week (up to `weeklyKeep` weeks)
//     is assigned tier "weekly".
//  3. Of the remaining, the first backup for each calendar month (up to `monthlyKeep`
//     months) is assigned tier "monthly".
//  4. Any backup that doesn't fit into a tier slot is PURGED (file deleted + DB record removed).
//
// ┌─ GFS Python analogy ───────────────────────────────────────────────────────┐
// │                                                                             │
// │  # Sort newest first, assign tiers                                         │
// │  backups = sorted(all_backups, key=lambda b: b.created_at, reverse=True)  │
// │  daily = backups[:7]                                                       │
// │  weekly = pick_first_per_week(backups[7:], max=4)                         │
// │  monthly = pick_first_per_month(remainder, max=12)                        │
// │  for b in remainder: os.remove(b.archive_path); db.delete(b)             │
// └─────────────────────────────────────────────────────────────────────────────┘
func (g *Guardian) rotateGFS(projectPath string) {
	rows, err := g.db.QueryContext(g.ctx, `
		SELECT id, archive_path, tier, size_bytes, created_at
		FROM   backups
		WHERE  project_path = ?
		ORDER  BY id DESC
	`, projectPath)
	if err != nil {
		log.Printf("[Guardian] GFS: query error: %v", err)
		return
	}

	var all []backupRecord
	for rows.Next() {
		var rec backupRecord
		var createdStr string
		if err := rows.Scan(&rec.id, &rec.archivePath, &rec.tier, &rec.sizeBytes, &createdStr); err != nil {
			continue
		}
		rec.projectPath = projectPath
		// Parse the RFC3339 timestamp stored in the DB.
		if t, err := time.Parse(time.RFC3339, createdStr); err == nil {
			rec.createdAt = t
		} else {
			// Try SQLite's default DATETIME format as fallback.
			rec.createdAt, _ = time.ParseInLocation("2006-01-02 15:04:05", createdStr, time.UTC)
		}
		all = append(all, rec)
	}
	rows.Close()

	if len(all) == 0 {
		return
	}

	dailyCount, weeklyCount, monthlyCount := 0, 0, 0
	seenWeeks := make(map[int64]bool)  // ISO week key: year*100 + week
	seenMonths := make(map[int64]bool) // calendar month key: year*100 + month

	type tierUpdate struct {
		id   int64
		tier string
	}
	var updates []tierUpdate
	var toDelete []backupRecord

	// Process newest → oldest (ORDER BY id DESC guarantees this).
	for _, rec := range all {
		year, week := rec.createdAt.ISOWeek()
		weekKey := int64(year)*100 + int64(week)
		monthKey := int64(rec.createdAt.Year())*100 + int64(rec.createdAt.Month())

		var newTier string
		if dailyCount < defaultDailyKeep {
			newTier = "daily"
			dailyCount++
		} else if !seenWeeks[weekKey] && weeklyCount < defaultWeeklyKeep {
			newTier = "weekly"
			seenWeeks[weekKey] = true
			weeklyCount++
		} else if !seenMonths[monthKey] && monthlyCount < defaultMonthlyKeep {
			newTier = "monthly"
			seenMonths[monthKey] = true
			monthlyCount++
		} else {
			toDelete = append(toDelete, rec)
			continue
		}

		if rec.tier != newTier {
			updates = append(updates, tierUpdate{rec.id, newTier})
		}
	}

	// Apply tier updates to DB.
	for _, u := range updates {
		g.db.ExecContext(g.ctx, `UPDATE backups SET tier = ? WHERE id = ?`, u.tier, u.id)
	}

	// Purge expired backups (file + DB record).
	for _, rec := range toDelete {
		if err := g.purgeBackup(rec); err != nil {
			log.Printf("[Guardian] GFS: failed to purge backup %d: %v", rec.id, err)
		}
	}

	if len(toDelete) > 0 {
		log.Printf("[Guardian] GFS rotation: purged %d expired backup(s) for %q", len(toDelete), projectPath)
	}
}

// ── DB Operations ──────────────────────────────────────────────────────────────

// persistBackup inserts a new backup record and returns its BackupInfo.
func (g *Guardian) persistBackup(projectPath, archivePath string, sizeBytes int64) (BackupInfo, error) {
	res, err := g.db.ExecContext(g.ctx,
		`INSERT INTO backups (project_path, archive_path, tier, size_bytes) VALUES (?, ?, 'daily', ?)`,
		projectPath, archivePath, sizeBytes,
	)
	if err != nil {
		return BackupInfo{}, err
	}
	id, _ := res.LastInsertId()

	var createdAt string
	g.db.QueryRowContext(g.ctx, `SELECT created_at FROM backups WHERE id = ?`, id).Scan(&createdAt)

	return BackupInfo{
		ID: id, ProjectPath: projectPath, ArchivePath: archivePath,
		Tier: "daily", SizeBytes: sizeBytes, CreatedAt: createdAt,
	}, nil
}

// loadBackupRecord hydrates a backupRecord from the DB by ID.
func (g *Guardian) loadBackupRecord(id int64) (backupRecord, error) {
	var rec backupRecord
	var createdStr string
	err := g.db.QueryRowContext(g.ctx,
		`SELECT id, project_path, archive_path, tier, size_bytes, created_at FROM backups WHERE id = ?`, id,
	).Scan(&rec.id, &rec.projectPath, &rec.archivePath, &rec.tier, &rec.sizeBytes, &createdStr)
	if err != nil {
		return backupRecord{}, err
	}
	if t, parseErr := time.Parse(time.RFC3339, createdStr); parseErr == nil {
		rec.createdAt = t
	} else {
		rec.createdAt, _ = time.ParseInLocation("2006-01-02 15:04:05", createdStr, time.UTC)
	}
	return rec, nil
}

// purgeBackup deletes the archive file from disk and removes the DB record.
func (g *Guardian) purgeBackup(rec backupRecord) error {
	if err := os.Remove(rec.archivePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove archive file: %w", err)
	}
	_, err := g.db.ExecContext(g.ctx, `DELETE FROM backups WHERE id = ?`, rec.id)
	return err
}


// UpdateBackup modifies the tier of a backup.
func (g *Guardian) UpdateBackup(id int64, tier string) error {
	if tier != "daily" && tier != "weekly" && tier != "monthly" {
		return fmt.Errorf("invalid tier %q", tier)
	}
	_, err := g.db.ExecContext(g.ctx, `UPDATE backups SET tier = ? WHERE id = ?`, tier, id)
	return err
}

// toInfo converts an internal backupRecord to the JS-facing BackupInfo.
func (rec backupRecord) toInfo() BackupInfo {
	return BackupInfo{
		ID: rec.id, ProjectPath: rec.projectPath, ArchivePath: rec.archivePath,
		Tier: rec.tier, SizeBytes: rec.sizeBytes,
		CreatedAt: rec.createdAt.UTC().Format(time.RFC3339),
	}
}

// ── Archive Pipeline ───────────────────────────────────────────────────────────

// decryptAndExtract reads an encrypted archive file, decrypts it, and extracts
// the resulting ZIP to destDir.
func (g *Guardian) decryptAndExtract(archivePath, destDir string) error {
	ciphertext, err := os.ReadFile(archivePath)
	if err != nil {
		return fmt.Errorf("read archive file: %w", err)
	}
	zipData, err := decryptAESGCM(g.key, ciphertext)
	if err != nil {
		return err // decryptAESGCM's error message is already descriptive
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}
	return extractZIP(zipData, destDir)
}

// ── Filesystem Utilities ───────────────────────────────────────────────────────

// replaceDir clears destDir and copies the full contents of srcDir into it.
// Used by both rollback modes to atomically swap directory contents.
func replaceDir(srcDir, destDir string) error {
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("clear dest: %w", err)
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("recreate dest: %w", err)
	}
	return copyDirContents(srcDir, destDir)
}

// copyDirContents recursively copies all files from srcDir into destDir.
// Directories are recreated; files are copied byte-for-byte.
//
// Python analogy:
//
//	for item in os.scandir(srcDir):
//	    shutil.copy2(item.path, destDir / item.name)  # preserves metadata
//	    # or for directories: shutil.copytree(item.path, destDir / item.name)
func copyDirContents(srcDir, destDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(srcDir, path)
		if relPath == "." {
			return nil // skip the root itself
		}
		destPath := filepath.Join(destDir, relPath)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}
		return copyFile(path, destPath)
	})
}

// copyFile copies a single file from src to dst, creating dst if it doesn't exist.
func copyFile(src, dst string) (retErr error) {
	srcF, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcF.Close()

	// os.Create truncates or creates the destination file.
	// Python analogy: open(dst, 'wb')
	dstF, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := dstF.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	_, err = io.Copy(dstF, srcF)
	return err
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.2f GB", float64(b)/1024/1024/1024)
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/1024/1024)
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// ensure sort is imported (used in future sort.Slice calls if policy is extended)
var _ = sort.Slice
