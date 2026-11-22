package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Store manages the SQLite WAL persistence layer with separated read/write connection pools.
type Store struct {
	path    string
	writeDB *sql.DB
	readDB  *sql.DB
}

// Open initializes a SQLite WAL store at path (or ":memory:" for ephemeral tests) and runs embedded migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store: empty database path")
	}

	isMemory := path == ":memory:"
	if !isMemory {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("store: resolve path %q: %w", path, err)
		}
		path = abs
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("store: create parent dir: %w", err)
		}
	}

	writeDSN := buildDSN(path, isMemory, false)
	writeDB, err := sql.Open("sqlite", writeDSN)
	if err != nil {
		return nil, fmt.Errorf("store: open writer db: %w", err)
	}
	// Single writer connection serializes writes without intra-process SQLITE_BUSY lock upgrades.
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)

	if err := writeDB.PingContext(ctx); err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("store: ping writer db: %w", err)
	}

	var readDB *sql.DB
	if isMemory {
		readDB = writeDB
	} else {
		readDSN := buildDSN(path, false, true)
		readDB, err = sql.Open("sqlite", readDSN)
		if err != nil {
			_ = writeDB.Close()
			return nil, fmt.Errorf("store: open reader db: %w", err)
		}
		readDB.SetMaxOpenConns(4)
		readDB.SetMaxIdleConns(4)
		if err := readDB.PingContext(ctx); err != nil {
			_ = readDB.Close()
			_ = writeDB.Close()
			return nil, fmt.Errorf("store: ping reader db: %w", err)
		}
	}

	s := &Store{
		path:    path,
		writeDB: writeDB,
		readDB:  readDB,
	}

	if err := s.migrate(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}

	return s, nil
}

// Path returns the resolved database path (or ":memory:").
func (s *Store) Path() string {
	return s.path
}

// Close closes the underlying reader and writer SQLite connection pools.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.readDB != nil && s.readDB != s.writeDB {
		if err := s.readDB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close reader db: %w", err))
		}
	}
	if s.writeDB != nil {
		if err := s.writeDB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close writer db: %w", err))
		}
	}
	return errors.Join(errs...)
}

func buildDSN(path string, isMemory, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	if !isMemory {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(NORMAL)")
	}
	if readOnly {
		q.Add("_pragma", "query_only(ON)")
	}
	if isMemory {
		return ":memory:?" + q.Encode()
	}
	slash := filepath.ToSlash(path)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	u := &url.URL{
		Scheme:   "file",
		Path:     slash,
		RawQuery: q.Encode(),
	}
	return u.String()
}
