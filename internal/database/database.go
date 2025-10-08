package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/sirupsen/logrus"
)

const (
	createTableStmt = `
	CREATE TABLE IF NOT EXISTS honeypot (
		ip_address TEXT PRIMARY KEY,
		attempts INTEGER NOT NULL,
		last_attempt_at DATETIME NOT NULL,
		banned_at DATETIME
	);`
	upsertAttemptStmt = `
	INSERT INTO honeypot (ip_address, attempts, last_attempt_at)
	VALUES (?, 1, ?)
	ON CONFLICT(ip_address) DO UPDATE SET
		attempts = attempts + 1,
		last_attempt_at = excluded.last_attempt_at;`
	queryAttemptsStmt = `SELECT attempts, banned_at FROM honeypot WHERE ip_address = ?;`
	banIPStmt         = `UPDATE honeypot SET banned_at = ? WHERE ip_address = ?;`
)

// HoneypotDB handles database operations for the honeypot
type HoneypotDB struct {
	db     *sql.DB
	logger *logrus.Logger
	mu     sync.RWMutex
}

// NewHoneypotDB creates and initializes a new HoneypotDB
func NewHoneypotDB(dbPath string, logger *logrus.Logger) (*HoneypotDB, error) {
	// Ensure the directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(createTableStmt); err != nil {
		return nil, err
	}

	return &HoneypotDB{
		db:     db,
		logger: logger,
	}, nil
}

// RecordFailedAttempt records a failed access attempt for a given IP
func (h *HoneypotDB) RecordFailedAttempt(ip string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now().UTC()

	// Upsert the attempt count
	if _, err := h.db.Exec(upsertAttemptStmt, ip, now); err != nil {
		return 0, err
	}

	// Get the new attempt count
	var attempts int
	var bannedAt sql.NullTime
	err := h.db.QueryRow(queryAttemptsStmt, ip).Scan(&attempts, &bannedAt)
	if err != nil {
		return 0, err
	}

	return attempts, nil
}

// IsBanned checks if an IP is currently banned
func (h *HoneypotDB) IsBanned(ip string, banThreshold int) (bool, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var attempts int
	var bannedAt sql.NullTime

	err := h.db.QueryRow(queryAttemptsStmt, ip).Scan(&attempts, &bannedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil // Not in the DB, so not banned
		}
		return false, err
	}

	// If already marked as banned, it's banned
	if bannedAt.Valid {
		return true, nil
	}

	// If not marked but exceeds threshold, ban it now
	if attempts >= banThreshold {
		if err := h.banIP(ip); err != nil {
			h.logger.WithError(err).WithField("ip", ip).Error("Failed to mark IP as banned")
			return true, err // Still treat as banned even if the DB write fails
		}
		return true, nil
	}

	return false, nil
}

// banIP marks an IP as banned in the database.
// This is an internal helper and should be called from within a locked section.
func (h *HoneypotDB) banIP(ip string) error {
	now := time.Now().UTC()
	_, err := h.db.Exec(banIPStmt, now, ip)
	return err
}

// Close closes the database connection
func (h *HoneypotDB) Close() error {
	return h.db.Close()
}
