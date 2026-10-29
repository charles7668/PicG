package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"git.charleshnas.dscloud.me/charles7668/PicG/internal/migrate"
	_ "modernc.org/sqlite"
)

const maxOpenConnections = 8

func Open(path string) (*sql.DB, error) {
	return OpenWith(path, migrate.Host{DatabasePath: path})
}

func OpenWith(path string, host migrate.Host) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	databasePath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && databasePath != "" && databasePath[0] != '/' {
		databasePath = "/" + databasePath
	}
	dsn := url.URL{Scheme: "file", Path: databasePath}
	parameters := dsn.Query()
	parameters.Set("_busy_timeout", "5000")
	parameters.Set("_foreign_keys", "on")
	parameters.Set("_journal_mode", "wal")
	dsn.RawQuery = parameters.Encode()

	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// The DSN applies connection-local PRAGMAs to every connection in the pool.
	// Keep all eight connections idle so bursts of thumbnail reads can reuse
	// them instead of repeatedly opening SQLite connections.
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxOpenConnections)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure database: %w", err)
	}
	host.DB = db
	if host.DatabasePath == "" {
		host.DatabasePath = path
	}
	if err := migrate.Run(context.Background(), host, migrate.All); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
