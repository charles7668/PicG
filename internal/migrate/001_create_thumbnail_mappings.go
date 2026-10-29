package migrate

import "context"

type CreateThumbnailMappings struct{}

func (CreateThumbnailMappings) Version() int {
	return 1
}

func (CreateThumbnailMappings) Up(ctx context.Context, host Host) error {
	_, err := host.DB.ExecContext(ctx, `
CREATE TABLE thumbnail_mappings (
    id                INTEGER PRIMARY KEY,
    source_kind       TEXT NOT NULL CHECK (source_kind IN ('image', 'video', 'cover')),
    source_path       TEXT NOT NULL,
    source_file_path  TEXT NOT NULL,
    source_hash       TEXT NOT NULL,
    file_name         TEXT NOT NULL UNIQUE,
    source_size       INTEGER NOT NULL,
    source_mtime_ns   INTEGER NOT NULL,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    last_access_at    INTEGER NOT NULL,
    UNIQUE (source_kind, source_path)
);
CREATE INDEX thumbnail_mappings_source_file_idx ON thumbnail_mappings (source_file_path);
CREATE INDEX thumbnail_mappings_last_access_idx ON thumbnail_mappings (last_access_at);
`)
	return err
}
