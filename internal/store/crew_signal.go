package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxCrewSignalNoteRunes = 140

var (
	ErrCrewSignalUnavailable  = errors.New("crew_signal_unavailable")
	ErrCrewSignalAlreadyToday = errors.New("crew_signal_already_today")
)

// CrewSignalStats is the public demand-beacon counter.
type CrewSignalStats struct {
	WeekCount  int64
	TotalCount int64
}

// InsertCrewSignal records one demand signal per project per UTC day.
// note is trimmed and capped at MaxCrewSignalNoteRunes.
func (s *Store) InsertCrewSignal(ctx context.Context, projectID uuid.UUID, login, note string, now time.Time) error {
	if s.Dialect() == DialectSQLite {
		return ErrCrewSignalUnavailable
	}
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > MaxCrewSignalNoteRunes {
		r := []rune(note)
		note = string(r[:MaxCrewSignalNoteRunes])
	}
	login = strings.TrimSpace(login)
	id := uuid.New()
	_, err := s.db.Exec(ctx, `
		INSERT INTO crew_signals (id, project_id, login, note, signal_day, created_at)
		VALUES ($1, $2, $3, $4, $5::date, $6)
	`, id, projectID, login, note, day, now)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrCrewSignalAlreadyToday
		}
		return fmt.Errorf("insert crew_signal: %w", err)
	}
	return nil
}

// CountCrewSignals returns week (UTC rolling 7d) and all-time totals.
func (s *Store) CountCrewSignals(ctx context.Context, now time.Time) (CrewSignalStats, error) {
	if s.Dialect() == DialectSQLite {
		return CrewSignalStats{}, nil
	}
	now = now.UTC()
	weekStart := now.Add(-7 * 24 * time.Hour)
	var st CrewSignalStats
	err := s.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE created_at >= $1)::bigint,
			COUNT(*)::bigint
		FROM crew_signals
	`, weekStart).Scan(&st.WeekCount, &st.TotalCount)
	if err != nil {
		return CrewSignalStats{}, fmt.Errorf("count crew_signals: %w", err)
	}
	return st, nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}
