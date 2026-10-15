package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/db"
	"github.com/google/uuid"
)

func TestCrewSignalSQLiteUnavailable(t *testing.T) {
	dir := t.TempDir()
	url := "sqlite:" + filepath.Join(dir, "t.db")
	if err := db.Migrate(url); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.ConnectSQLite(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := NewSQLite(sqlDB)
	err = st.InsertCrewSignal(context.Background(), uuid.New(), "tester", "note", time.Now().UTC())
	if err != ErrCrewSignalUnavailable {
		t.Fatalf("want ErrCrewSignalUnavailable, got %v", err)
	}
	stats, err := st.CountCrewSignals(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if stats.WeekCount != 0 || stats.TotalCount != 0 {
		t.Fatalf("sqlite counts should be zero: %+v", stats)
	}
}
