package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// FreezeInfo describes the holder of an active sync freeze.
type FreezeInfo struct {
	Actor     string
	Reason    string
	CreatedAt time.Time
}

// FreezeHeldError is returned by sync entry points when the global sync
// freeze is held. The CLI maps this to a non-zero exit with a friendly
// message that includes the unlock command.
type FreezeHeldError struct {
	Info FreezeInfo
}

func (e *FreezeHeldError) Error() string {
	return fmt.Sprintf("sync is frozen by %s: %s (held since %s) — release with: pbflags unlock",
		e.Info.Actor, e.Info.Reason, e.Info.CreatedAt.Format(time.RFC3339))
}

// IsFreezeHeld unwraps an error chain looking for a FreezeHeldError.
func IsFreezeHeld(err error) (*FreezeHeldError, bool) {
	var f *FreezeHeldError
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

// queryRower is implemented by *pgx.Conn and pgx.Tx — both can run a
// single-row query.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// checkFreeze returns a *FreezeHeldError when the global sync freeze is
// held. Callers should invoke this BEFORE any write tx so the gate fails
// loudly with no side effects.
func checkFreeze(ctx context.Context, q queryRower) error {
	var info FreezeInfo
	err := q.QueryRow(ctx,
		`SELECT actor, reason, created_at FROM feature_flags.sync_freeze WHERE id = 1`).
		Scan(&info.Actor, &info.Reason, &info.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check sync freeze: %w", err)
	}
	return &FreezeHeldError{Info: info}
}

// auto-clear constants. Mirrored from internal/admin/overrides.go to avoid
// pulling that package's many transitive deps into the sync path.
const (
	autoClearAction = "CONDITION_OVERRIDE_AUTO_CLEARED"
	autoClearActor  = "system:sync"
)

// clearOverridesForFlagsTx deletes any condition_overrides for the given
// flag IDs and writes one audit entry per cleared row. Runs inside the
// provided sync transaction so the clear and the new conditions commit
// atomically.
//
// flagIDs may be empty — that's a no-op. syncReason is included in each
// audit entry so operators can see "auto-cleared by sync of <feature>".
func clearOverridesForFlagsTx(ctx context.Context, tx pgx.Tx, flagIDs []string, syncReason string) (int, error) {
	if len(flagIDs) == 0 {
		return 0, nil
	}

	// Read first so we can audit-log each row being cleared.
	rows, err := tx.Query(ctx,
		`SELECT flag_id, condition_index, override_value, source, actor
		 FROM feature_flags.condition_overrides
		 WHERE flag_id = ANY($1::varchar[])`, flagIDs)
	if err != nil {
		return 0, fmt.Errorf("read condition_overrides for clear: %w", err)
	}

	type cleared struct {
		flagID   string
		condIdx  *int32
		valueRaw []byte
		source   string
		actor    string
	}
	var rowsToClear []cleared
	for rows.Next() {
		var c cleared
		if err := rows.Scan(&c.flagID, &c.condIdx, &c.valueRaw, &c.source, &c.actor); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan condition_overrides: %w", err)
		}
		rowsToClear = append(rowsToClear, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate condition_overrides: %w", err)
	}
	if len(rowsToClear) == 0 {
		return 0, nil
	}

	// Delete the rows in one statement.
	if _, err := tx.Exec(ctx,
		`DELETE FROM feature_flags.condition_overrides WHERE flag_id = ANY($1::varchar[])`,
		flagIDs); err != nil {
		return 0, fmt.Errorf("delete condition_overrides: %w", err)
	}

	// Audit each cleared override.
	for _, c := range rowsToClear {
		if err := writeAutoClearAudit(ctx, tx, c.flagID, c.condIdx, c.valueRaw, c.source, c.actor, syncReason); err != nil {
			return 0, err
		}
	}
	return len(rowsToClear), nil
}

// writeAutoClearAudit appends one CONDITION_OVERRIDE_AUTO_CLEARED entry
// to flag_audit_log. The previous override value goes in old_value;
// new_value carries a short human-readable note (matching the
// stringValueProto pattern used by admin/overrides.go) so operators can
// reconstruct what was cleared.
func writeAutoClearAudit(ctx context.Context, tx pgx.Tx, flagID string, condIdx *int32, prevValueBytes []byte, prevSource, prevActor, syncReason string) error {
	condDesc := "default"
	if condIdx != nil {
		condDesc = fmt.Sprintf("condition[%d]", *condIdx)
	}
	note := fmt.Sprintf("auto-cleared %s by sync%s (was %s/%s)",
		condDesc, reasonSuffix(syncReason), prevSource, prevActor)
	newValue := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: note}}
	newValueBytes, err := proto.Marshal(newValue)
	if err != nil {
		return fmt.Errorf("marshal auto-clear audit note: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO feature_flags.flag_audit_log
		    (flag_id, action, old_value, new_value, actor)
		VALUES ($1, $2, $3, $4, $5)`,
		flagID, autoClearAction, prevValueBytes, newValueBytes, autoClearActor)
	if err != nil {
		return fmt.Errorf("insert auto-clear audit: %w", err)
	}
	return nil
}

func reasonSuffix(r string) string {
	if r == "" {
		return ""
	}
	return ": " + r
}
