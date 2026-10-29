package migrate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type recordingMigration struct {
	version int
	applied *[]int
	up      func(ctx context.Context, host Host) error
}

func (m recordingMigration) Version() int {
	return m.version
}

func (m recordingMigration) Up(ctx context.Context, host Host) error {
	if m.applied != nil {
		*m.applied = append(*m.applied, m.version)
	}
	if m.up != nil {
		return m.up(ctx, host)
	}
	return nil
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRunAppliesPendingVersionsInOrder(t *testing.T) {
	host := Host{DB: openTestDB(t)}
	var applied []int
	steps := []Migration{
		recordingMigration{version: 1, applied: &applied},
		recordingMigration{version: 2, applied: &applied},
		recordingMigration{version: 3, applied: &applied},
	}
	if err := Run(context.Background(), host, steps[:2]); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), host, steps); err != nil {
		t.Fatal(err)
	}
	if got, want := fmtInts(applied), "1,2,3"; got != want {
		t.Fatalf("applied versions %s, want %s", got, want)
	}
	var version int
	if err := host.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("schema version %d, want 3", version)
	}
}

func TestRunSkipsWhenAlreadyCurrent(t *testing.T) {
	host := Host{DB: openTestDB(t)}
	var applied []int
	steps := []Migration{
		recordingMigration{version: 1, applied: &applied},
		recordingMigration{version: 2, applied: &applied},
	}
	if err := Run(context.Background(), host, steps); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), host, steps); err != nil {
		t.Fatal(err)
	}
	if got, want := fmtInts(applied), "1,2"; got != want {
		t.Fatalf("applied versions %s, want %s", got, want)
	}
}

func TestRunRejectsNewerDatabase(t *testing.T) {
	host := Host{DB: openTestDB(t)}
	if err := Run(context.Background(), host, []Migration{
		recordingMigration{version: 1},
		recordingMigration{version: 2},
	}); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), host, []Migration{
		recordingMigration{version: 1},
	})
	if err == nil {
		t.Fatal("expected newer schema error")
	}
}

func TestRunRejectsNonConsecutiveVersions(t *testing.T) {
	err := Run(context.Background(), Host{DB: openTestDB(t)}, []Migration{
		recordingMigration{version: 1},
		recordingMigration{version: 3},
	})
	if err == nil {
		t.Fatal("expected non-consecutive version error")
	}
}

func TestRunDoesNotRecordFailedStep(t *testing.T) {
	host := Host{DB: openTestDB(t)}
	var applied []int
	err := Run(context.Background(), host, []Migration{
		recordingMigration{version: 1, applied: &applied},
		recordingMigration{version: 2, applied: &applied, up: func(context.Context, Host) error {
			return errors.New("boom")
		}},
	})
	if err == nil {
		t.Fatal("expected apply error")
	}
	if got, want := fmtInts(applied), "1,2"; got != want {
		t.Fatalf("applied versions %s, want %s", got, want)
	}
	var version int
	if err := host.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("schema version %d, want 1 after failed step", version)
	}
}

func TestRunAllowsFileChanges(t *testing.T) {
	work := t.TempDir()
	thumbnailDir := filepath.Join(work, "thumbnails")
	if err := os.MkdirAll(thumbnailDir, 0o755); err != nil {
		t.Fatal(err)
	}
	host := Host{
		DB:           openTestDB(t),
		DatabasePath: filepath.Join(work, "app.sqlite"),
		ThumbnailDir: thumbnailDir,
	}
	marker := filepath.Join(thumbnailDir, "migrated")
	err := Run(context.Background(), host, []Migration{
		recordingMigration{version: 1, up: func(_ context.Context, host Host) error {
			return os.WriteFile(filepath.Join(host.Thumbnails(), "migrated"), []byte("ok"), 0o644)
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("unexpected file contents %q", data)
	}
}

func fmtInts(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}
