package migrate

import "context"

type CreateVideoInfo struct{}

func (CreateVideoInfo) Version() int {
	return 3
}

func (CreateVideoInfo) Up(ctx context.Context, host Host) error {
	_, err := host.DB.ExecContext(ctx, `
CREATE TABLE video_info (
    source_path       TEXT PRIMARY KEY,
    source_file_path  TEXT NOT NULL,
    source_size       INTEGER NOT NULL,
    source_mtime_ns   INTEGER NOT NULL,
    duration          REAL NOT NULL,
    direct            INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);
`)
	return err
}
