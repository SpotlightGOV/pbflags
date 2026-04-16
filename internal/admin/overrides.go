package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// Audit-log action constants for the lock + condition-override epic.
// Defined here (rather than as bare strings) so they're greppable and
// callers can't typo them.
const (
	ActionAcquireSyncLock            = "ACQUIRE_SYNC_LOCK"
	ActionReleaseSyncLock            = "RELEASE_SYNC_LOCK"
	ActionSetConditionOverride       = "SET_CONDITION_OVERRIDE"
	ActionClearConditionOverride     = "CLEAR_CONDITION_OVERRIDE"
	ActionClearAllConditionOverrides = "CLEAR_ALL_CONDITION_OVERRIDES"
	ActionConditionOverrideAutoClear = "CONDITION_OVERRIDE_AUTO_CLEARED"
)

// auditFlagIDSyncLock is a sentinel value used in the flag_audit_log.flag_id
// column for global-scope audit entries that aren't tied to any single flag.
const auditFlagIDSyncLock = "__sync_lock__"

// SyncLockInfo describes the held state of the global config-sync lock.
type SyncLockInfo struct {
	Actor     string
	Reason    string
	CreatedAt time.Time
}

// SyncLockHeldError is returned when AcquireSyncLock is called while the
// lock is already held by someone else. It carries the current holder so
// the caller can include it in user-facing error messages.
type SyncLockHeldError struct {
	Info SyncLockInfo
}

func (e *SyncLockHeldError) Error() string {
	return fmt.Sprintf("sync is already locked by %s: %s", e.Info.Actor, e.Info.Reason)
}

// ErrSyncNotLocked is returned when ReleaseSyncLock is called while
// the lock is not held.
var ErrSyncNotLocked = errors.New("sync is not locked")

// AcquireSyncLock takes the global sync lock. If the lock is already
// held, returns *SyncLockHeldError carrying the current holder's metadata.
func (s *Store) AcquireSyncLock(ctx context.Context, actor, reason string) (*SyncLockInfo, error) {
	if actor == "" {
		return nil, fmt.Errorf("actor is required")
	}
	if reason == "" {
		return nil, fmt.Errorf("reason is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := getSyncLockTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if current != nil {
		return nil, &SyncLockHeldError{Info: *current}
	}

	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO feature_flags.sync_lock (id, actor, reason)
		VALUES (1, $1, $2)
		RETURNING created_at`, actor, reason).Scan(&createdAt); err != nil {
		return nil, fmt.Errorf("insert sync_lock: %w", err)
	}

	if err := insertAuditEntry(ctx, tx, auditFlagIDSyncLock,
		ActionAcquireSyncLock, nil, stringValueProto(reason), actor); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &SyncLockInfo{Actor: actor, Reason: reason, CreatedAt: createdAt}, nil
}

// ReleaseSyncLock releases the global sync lock. Returns
// ErrSyncNotLocked if no lock is currently held.
func (s *Store) ReleaseSyncLock(ctx context.Context, actor string) error {
	if actor == "" {
		return fmt.Errorf("actor is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := getSyncLockTx(ctx, tx)
	if err != nil {
		return err
	}
	if current == nil {
		return ErrSyncNotLocked
	}

	if _, err := tx.Exec(ctx, `DELETE FROM feature_flags.sync_lock WHERE id = 1`); err != nil {
		return fmt.Errorf("delete sync_lock: %w", err)
	}

	heldFor := time.Since(current.CreatedAt).Truncate(time.Second).String()
	auditMsg := fmt.Sprintf("unlocked after %s; reason was: %s", heldFor, current.Reason)
	if err := insertAuditEntry(ctx, tx, auditFlagIDSyncLock,
		ActionReleaseSyncLock, stringValueProto(current.Reason), stringValueProto(auditMsg), actor); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// GetSyncLock returns the current lock state, or (nil, nil) if unlocked.
func (s *Store) GetSyncLock(ctx context.Context) (*SyncLockInfo, error) {
	return getSyncLock(ctx, s.pool)
}

// getSyncLock and getSyncLockTx allow both pool and tx callers.
type slQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func getSyncLock(ctx context.Context, q slQuerier) (*SyncLockInfo, error) {
	var info SyncLockInfo
	err := q.QueryRow(ctx, `SELECT actor, reason, created_at FROM feature_flags.sync_lock WHERE id = 1`).
		Scan(&info.Actor, &info.Reason, &info.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query sync_lock: %w", err)
	}
	return &info, nil
}

func getSyncLockTx(ctx context.Context, tx pgx.Tx) (*SyncLockInfo, error) {
	return getSyncLock(ctx, tx)
}

// ── Condition overrides ─────────────────────────────────────────────

// ConditionOverride is one row from feature_flags.condition_overrides.
type ConditionOverride struct {
	FlagID         string
	ConditionIndex *int32 // nil = static/compiled-default override
	Value          *pbflagsv1.FlagValue
	Source         string // "cli" or "ui"
	Actor          string
	Reason         string
	CreatedAt      time.Time
}

// SetConditionOverride upserts an override row for (flagID, conditionIndex).
// Returns the previous override value at this position, if any (for
// confirmation UX). conditionIndex == nil means override the static/compiled
// default. source must be "cli" or "ui".
func (s *Store) SetConditionOverride(
	ctx context.Context,
	flagID string,
	conditionIndex *int32,
	value *pbflagsv1.FlagValue,
	source, actor, reason string,
) (*pbflagsv1.FlagValue, error) {
	if flagID == "" {
		return nil, fmt.Errorf("flag_id is required")
	}
	if value == nil {
		return nil, fmt.Errorf("value is required")
	}
	if reason == "" {
		return nil, fmt.Errorf("reason is required")
	}
	if actor == "" {
		return nil, fmt.Errorf("actor is required")
	}
	if source != "cli" && source != "ui" {
		return nil, fmt.Errorf("source must be 'cli' or 'ui', got %q", source)
	}

	valBytes, err := proto.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal value: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Verify the flag exists. Returns NotFound semantics to caller via error.
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM feature_flags.flags WHERE flag_id = $1)`, flagID,
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check flag exists: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("flag %s not found", flagID)
	}

	// Read the previous override at this position (if any).
	prev, err := readOverrideValueTx(ctx, tx, flagID, conditionIndex)
	if err != nil {
		return nil, err
	}

	// Upsert. We can't use ON CONFLICT cleanly with partial unique indexes,
	// so do an UPDATE-then-INSERT.
	if prev != nil {
		if conditionIndex == nil {
			_, err = tx.Exec(ctx, `
				UPDATE feature_flags.condition_overrides
				SET override_value = $2, source = $3, actor = $4, reason = $5, created_at = now()
				WHERE flag_id = $1 AND condition_index IS NULL`,
				flagID, valBytes, source, actor, reason)
		} else {
			_, err = tx.Exec(ctx, `
				UPDATE feature_flags.condition_overrides
				SET override_value = $2, source = $3, actor = $4, reason = $5, created_at = now()
				WHERE flag_id = $1 AND condition_index = $6`,
				flagID, valBytes, source, actor, reason, *conditionIndex)
		}
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO feature_flags.condition_overrides
				(flag_id, condition_index, override_value, source, actor, reason)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			flagID, conditionIndex, valBytes, source, actor, reason)
	}
	if err != nil {
		return nil, fmt.Errorf("upsert condition_override: %w", err)
	}

	if err := insertAuditEntry(ctx, tx, flagID,
		ActionSetConditionOverride, prev, value, actor); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return prev, nil
}

// ClearConditionOverride deletes a single override row. Returns
// pgx.ErrNoRows-equivalent error wrapped with a NotFound-friendly message
// when the override doesn't exist.
func (s *Store) ClearConditionOverride(
	ctx context.Context,
	flagID string,
	conditionIndex *int32,
	actor string,
) error {
	if flagID == "" {
		return fmt.Errorf("flag_id is required")
	}
	if actor == "" {
		return fmt.Errorf("actor is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	prev, err := readOverrideValueTx(ctx, tx, flagID, conditionIndex)
	if err != nil {
		return err
	}
	if prev == nil {
		return fmt.Errorf("no override exists for flag %s at condition %s", flagID, formatCondIndex(conditionIndex))
	}

	if conditionIndex == nil {
		_, err = tx.Exec(ctx,
			`DELETE FROM feature_flags.condition_overrides WHERE flag_id = $1 AND condition_index IS NULL`,
			flagID)
	} else {
		_, err = tx.Exec(ctx,
			`DELETE FROM feature_flags.condition_overrides WHERE flag_id = $1 AND condition_index = $2`,
			flagID, *conditionIndex)
	}
	if err != nil {
		return fmt.Errorf("delete condition_override: %w", err)
	}

	if err := insertAuditEntry(ctx, tx, flagID,
		ActionClearConditionOverride, prev, nil, actor); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ClearAllConditionOverrides deletes every override on a flag, returning the
// number cleared.
func (s *Store) ClearAllConditionOverrides(ctx context.Context, flagID, actor string) (int, error) {
	if flagID == "" {
		return 0, fmt.Errorf("flag_id is required")
	}
	if actor == "" {
		return 0, fmt.Errorf("actor is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`DELETE FROM feature_flags.condition_overrides WHERE flag_id = $1`, flagID)
	if err != nil {
		return 0, fmt.Errorf("delete condition_overrides: %w", err)
	}
	count := int(tag.RowsAffected())

	if count > 0 {
		summary := stringValueProto(fmt.Sprintf("cleared %d override(s)", count))
		if err := insertAuditEntry(ctx, tx, flagID,
			ActionClearAllConditionOverrides, nil, summary, actor); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return count, nil
}

// ListOverridesForFlag returns all overrides on a single flag, ordered with
// the static-default (NULL) row first, then by condition_index ascending.
func (s *Store) ListOverridesForFlag(ctx context.Context, flagID string) ([]ConditionOverride, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT flag_id, condition_index, override_value, source, actor, reason, created_at
		FROM feature_flags.condition_overrides
		WHERE flag_id = $1
		ORDER BY condition_index NULLS FIRST`, flagID)
	if err != nil {
		return nil, fmt.Errorf("query overrides: %w", err)
	}
	defer rows.Close()
	return scanOverrideRows(rows)
}

// OverrideListFilter narrows the global override listing.
type OverrideListFilter struct {
	FlagID string
	MinAge time.Duration
	Actor  string
}

// ListAllOverrides returns active condition overrides across all flags,
// newest first. Backs both the `pb overrides` CLI and the dashboard listing.
func (s *Store) ListAllOverrides(ctx context.Context, f OverrideListFilter) ([]ConditionOverride, error) {
	query := `
		SELECT flag_id, condition_index, override_value, source, actor, reason, created_at
		FROM feature_flags.condition_overrides
		WHERE 1=1`
	args := []any{}
	argN := 1
	if f.FlagID != "" {
		query += fmt.Sprintf(" AND flag_id = $%d", argN)
		args = append(args, f.FlagID)
		argN++
	}
	if f.MinAge > 0 {
		query += fmt.Sprintf(" AND created_at <= now() - make_interval(secs => $%d)", argN)
		args = append(args, f.MinAge.Seconds())
		argN++
	}
	if f.Actor != "" {
		query += fmt.Sprintf(" AND actor ILIKE $%d", argN)
		args = append(args, "%"+f.Actor+"%")
		argN++
	}
	query += " ORDER BY created_at DESC"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query overrides: %w", err)
	}
	defer rows.Close()
	return scanOverrideRows(rows)
}

// ClearOverridesForFlagsTx is invoked by sync inside its own transaction to
// auto-clear overrides for synced flags after a successful condition write.
// Each cleared override is audit-logged as CONDITION_OVERRIDE_AUTO_CLEARED.
//
// Caller passes the tx so the override clear commits atomically with the
// new conditions, eliminating the race where new conditions are visible but
// old overrides still match.
func (s *Store) ClearOverridesForFlagsTx(ctx context.Context, tx pgx.Tx, flagIDs []string, actor string) (int, error) {
	if len(flagIDs) == 0 {
		return 0, nil
	}

	rows, err := tx.Query(ctx, `
		DELETE FROM feature_flags.condition_overrides
		WHERE flag_id = ANY($1)
		RETURNING flag_id, condition_index, override_value`, flagIDs)
	if err != nil {
		return 0, fmt.Errorf("delete overrides for synced flags: %w", err)
	}

	type cleared struct {
		flagID  string
		condIdx *int32
		value   []byte
	}
	var deleted []cleared
	for rows.Next() {
		var c cleared
		if err := rows.Scan(&c.flagID, &c.condIdx, &c.value); err != nil {
			rows.Close()
			return 0, err
		}
		deleted = append(deleted, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, c := range deleted {
		oldVal, _ := unmarshalFlagValue(c.value)
		summary := stringValueProto(fmt.Sprintf("auto-cleared by sync (cond %s)", formatCondIndex(c.condIdx)))
		if err := insertAuditEntry(ctx, tx, c.flagID,
			ActionConditionOverrideAutoClear, oldVal, summary, actor); err != nil {
			return 0, err
		}
	}
	return len(deleted), nil
}

// ── Helpers ─────────────────────────────────────────────────────────

func readOverrideValueTx(ctx context.Context, tx pgx.Tx, flagID string, condIndex *int32) (*pbflagsv1.FlagValue, error) {
	var b []byte
	var err error
	if condIndex == nil {
		err = tx.QueryRow(ctx,
			`SELECT override_value FROM feature_flags.condition_overrides
			 WHERE flag_id = $1 AND condition_index IS NULL`, flagID).Scan(&b)
	} else {
		err = tx.QueryRow(ctx,
			`SELECT override_value FROM feature_flags.condition_overrides
			 WHERE flag_id = $1 AND condition_index = $2`, flagID, *condIndex).Scan(&b)
	}
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read prior override: %w", err)
	}
	return unmarshalFlagValue(b)
}

func scanOverrideRows(rows pgx.Rows) ([]ConditionOverride, error) {
	var out []ConditionOverride
	for rows.Next() {
		var o ConditionOverride
		var b []byte
		if err := rows.Scan(&o.FlagID, &o.ConditionIndex, &b, &o.Source, &o.Actor, &o.Reason, &o.CreatedAt); err != nil {
			return nil, err
		}
		v, err := unmarshalFlagValue(b)
		if err != nil {
			return nil, fmt.Errorf("unmarshal override_value: %w", err)
		}
		o.Value = v
		out = append(out, o)
	}
	return out, rows.Err()
}

// auditExec abstracts pool and tx so insertAuditEntry can run in either context.
type auditExec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// insertAuditEntry writes one row to feature_flags.flag_audit_log. oldVal and
// newVal may be nil; nil is stored as NULL (BYTEA NULL).
func insertAuditEntry(ctx context.Context, tx auditExec, flagID, action string, oldVal, newVal *pbflagsv1.FlagValue, actor string) error {
	var oldBytes, newBytes []byte
	var err error
	if oldVal != nil {
		oldBytes, err = proto.Marshal(oldVal)
		if err != nil {
			return fmt.Errorf("marshal audit old_value: %w", err)
		}
	}
	if newVal != nil {
		newBytes, err = proto.Marshal(newVal)
		if err != nil {
			return fmt.Errorf("marshal audit new_value: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO feature_flags.flag_audit_log (flag_id, action, old_value, new_value, actor)
		VALUES ($1, $2, $3, $4, $5)`,
		flagID, action, oldBytes, newBytes, actor,
	); err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

func stringValueProto(s string) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: s}}
}

func formatCondIndex(idx *int32) string {
	if idx == nil {
		return "default"
	}
	return fmt.Sprintf("%d", *idx)
}

// IsConfigManaged returns true when the owning feature has a non-empty
// sync_sha (i.e., config-as-code is currently the source of truth for this
// flag's conditions). Callers use this to surface a warning when overriding.
func (s *Store) IsConfigManaged(ctx context.Context, flagID string) (bool, error) {
	var sha *string
	err := s.pool.QueryRow(ctx, `
		SELECT ft.sync_sha
		FROM feature_flags.flags f
		LEFT JOIN feature_flags.features ft ON ft.feature_id = f.feature_id
		WHERE f.flag_id = $1`, flagID).Scan(&sha)
	if err == pgx.ErrNoRows {
		return false, fmt.Errorf("flag %s not found", flagID)
	}
	if err != nil {
		return false, fmt.Errorf("query flag: %w", err)
	}
	return sha != nil && strings.TrimSpace(*sha) != "", nil
}
