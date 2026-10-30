package video

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"git.charleshnas.dscloud.me/charles7668/PicG/internal/database"
)

func TestInfoStoreCachesProbe(t *testing.T) {
	work := t.TempDir()
	db, err := database.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewInfoStore(db)
	source := filepath.Join(work, "clip.mp4")
	if err := os.WriteFile(source, []byte("clip"), 0o644); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	probes := 0
	probe := func(context.Context) (Info, error) {
		probes++
		return Info{Duration: 1.25, Direct: true}, nil
	}
	first, err := store.Get(context.Background(), "clip.mp4", source, stat, probe)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Get(context.Background(), "clip.mp4", source, stat, probe)
	if err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatalf("expected one probe, got %d", probes)
	}
	if !first.Direct || first.Duration != 1.25 || second != first {
		t.Fatalf("unexpected info %#v %#v", first, second)
	}

	fresh := NewInfoStore(db)
	third, err := fresh.Get(context.Background(), "clip.mp4", source, stat, probe)
	if err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatalf("sql cache should skip probe, got %d", probes)
	}
	if third != first {
		t.Fatalf("sql cache mismatch %#v", third)
	}
}

func TestInfoStoreProbeDoesNotHoldCacheLock(t *testing.T) {
	work := t.TempDir()
	db, err := database.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewInfoStore(db)
	writeClip := func(name string) (string, os.FileInfo) {
		t.Helper()
		path := filepath.Join(work, name)
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return path, stat
	}
	slowPath, slowStat := writeClip("slow.mp4")
	fastPath, fastStat := writeClip("fast.mp4")

	started := make(chan struct{})
	release := make(chan struct{})
	slow := func(context.Context) (Info, error) {
		close(started)
		<-release
		return Info{Duration: 9, Direct: true}, nil
	}
	fast := func(context.Context) (Info, error) {
		return Info{Duration: 1, Direct: false}, nil
	}

	slowDone := make(chan error, 1)
	go func() {
		_, err := store.Get(context.Background(), "slow.mp4", slowPath, slowStat, slow)
		slowDone <- err
	}()
	<-started

	done := make(chan struct{})
	go func() {
		defer close(done)
		info, err := store.Get(context.Background(), "fast.mp4", fastPath, fastStat, fast)
		if err != nil {
			t.Errorf("fast get: %v", err)
			return
		}
		if info.Direct || info.Duration != 1 {
			t.Errorf("unexpected fast info %#v", info)
		}
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("other video blocked while a probe was in flight")
	}
	close(release)
	if err := <-slowDone; err != nil {
		t.Fatal(err)
	}
}

func TestInfoStoreSingleflightProbe(t *testing.T) {
	work := t.TempDir()
	db, err := database.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewInfoStore(db)
	source := filepath.Join(work, "clip.mp4")
	if err := os.WriteFile(source, []byte("clip"), 0o644); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	probe := func(context.Context) (Info, error) {
		if probes.Add(1) == 1 {
			close(started)
			<-release
		}
		return Info{Duration: 2, Direct: true}, nil
	}
	const n = 8
	results := make(chan Info, n)
	errs := make(chan error, n)
	for range n {
		go func() {
			info, err := store.Get(context.Background(), "clip.mp4", source, stat, probe)
			results <- info
			errs <- err
		}()
	}
	<-started
	close(release)
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if got := <-results; !got.Direct || got.Duration != 2 {
			t.Fatalf("unexpected info %#v", got)
		}
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("expected one probe for the same identity, got %d", got)
	}
}
