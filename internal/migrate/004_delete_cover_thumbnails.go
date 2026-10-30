package migrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// DeleteCoverThumbnails drops cover mappings made at the 240px cap so the
// next request renders them at the shared 640px size.
type DeleteCoverThumbnails struct{}

func (DeleteCoverThumbnails) Version() int {
	return 4
}

func (DeleteCoverThumbnails) Up(ctx context.Context, host Host) error {
	rows, err := host.DB.QueryContext(ctx, `SELECT file_name FROM thumbnail_mappings WHERE source_kind = 'cover'`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Remove files before the rows. A failure leaves the mappings in place so
	// the next attempt can still see the file names.
	if dir := host.Thumbnails(); dir != "" {
		for _, name := range names {
			if name == "" || name != filepath.Base(name) {
				return fmt.Errorf("cover thumbnail name %q", name)
			}
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	_, err = host.DB.ExecContext(ctx, `DELETE FROM thumbnail_mappings WHERE source_kind = 'cover'`)
	return err
}
