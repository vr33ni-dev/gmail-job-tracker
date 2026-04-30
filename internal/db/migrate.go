package db

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
)

func (s *Store) Migrate(dir string) error {
	// Create migrations tracking table if it doesn't exist
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	sort.Strings(files)

	for _, file := range files {
		filename := filepath.Base(file)

		// Skip if already applied
		var exists bool
		s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename=$1)`, filename).Scan(&exists)
		if exists {
			continue
		}

		content, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read %s: %w", filename, err)
		}

		if _, err := s.db.Exec(string(content)); err != nil {
			return fmt.Errorf("apply %s: %w", filename, err)
		}

		s.db.Exec(`INSERT INTO schema_migrations (filename) VALUES ($1)`, filename)
		log.Printf("applied migration: %s", filename)
	}

	return nil
}
