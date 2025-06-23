package database

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	_ "github.com/mattn/go-sqlite3"
)

// Database represents the SQLite database
type Database struct {
	db *sql.DB
}

// RefillRecord represents a record of a refill operation
type RefillRecord struct {
	ID        int64
	Address   string
	Amount    string
	TxHash    string
	Timestamp time.Time
	Success   bool
	Error     string
}

// ErrorRecord represents a record of an error
type ErrorRecord struct {
	ID        int64
	Address   string
	Error     string
	Timestamp time.Time
}

// New creates a new database connection
func New(path string) (*Database, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	database := &Database{db: db}
	if err := database.initialize(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	return database, nil
}

// Close closes the database connection
func (d *Database) Close() error {
	return d.db.Close()
}

// initialize creates the necessary tables if they don't exist
func (d *Database) initialize() error {
	// Create refills table
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS refills (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			address TEXT NOT NULL,
			amount TEXT NOT NULL,
			tx_hash TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			success BOOLEAN DEFAULT 0,
			error TEXT
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create refills table: %w", err)
	}

	// Create errors table
	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS errors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			address TEXT,
			error TEXT NOT NULL,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create errors table: %w", err)
	}

	return nil
}

// LogRefill logs a refill operation
func (d *Database) LogRefill(address common.Address, amount string, txHash string, success bool, errorMsg string) error {
	_, err := d.db.Exec(
		"INSERT INTO refills (address, amount, tx_hash, success, error) VALUES (?, ?, ?, ?, ?)",
		address.Hex(), amount, txHash, success, errorMsg,
	)
	return err
}

// LogError logs an error
func (d *Database) LogError(address common.Address, errorMsg string) error {
	_, err := d.db.Exec(
		"INSERT INTO errors (address, error) VALUES (?, ?)",
		address.Hex(), errorMsg,
	)
	return err
}

// GetRefillsInTimeWindow returns the number of refills for an address in a time window
func (d *Database) GetRefillsInTimeWindow(address common.Address, windowSeconds int) (int, error) {
	var count int
	err := d.db.QueryRow(
		"SELECT COUNT(*) FROM refills WHERE address = ? AND success = 1 AND timestamp > datetime('now', ?)",
		address.Hex(), fmt.Sprintf("-%d seconds", windowSeconds),
	).Scan(&count)
	return count, err
}

// GetRecentRefills returns the most recent refills
func (d *Database) GetRecentRefills(limit int) ([]RefillRecord, error) {
	rows, err := d.db.Query(
		"SELECT id, address, amount, tx_hash, timestamp, success, error FROM refills ORDER BY timestamp DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var refills []RefillRecord
	for rows.Next() {
		var r RefillRecord
		var timestamp string
		if err := rows.Scan(&r.ID, &r.Address, &r.Amount, &r.TxHash, &timestamp, &r.Success, &r.Error); err != nil {
			return nil, err
		}
		r.Timestamp, _ = time.Parse("2006-01-02 15:04:05", timestamp)
		refills = append(refills, r)
	}

	return refills, rows.Err()
}

// GetRecentErrors returns the most recent errors
func (d *Database) GetRecentErrors(limit int) ([]ErrorRecord, error) {
	rows, err := d.db.Query(
		"SELECT id, address, error, timestamp FROM errors ORDER BY timestamp DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var errors []ErrorRecord
	for rows.Next() {
		var e ErrorRecord
		var timestamp string
		if err := rows.Scan(&e.ID, &e.Address, &e.Error, &timestamp); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse("2006-01-02 15:04:05", timestamp)
		errors = append(errors, e)
	}

	return errors, rows.Err()
}
