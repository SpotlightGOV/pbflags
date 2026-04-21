package evaluator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

const contextExtNum protoreflect.FieldNumber = 51003 // (pbflags.context) on MessageOptions

// discoverContextDescriptor scans a file registry for a message annotated with
// option (pbflags.context). Returns the message descriptor, or an error if
// none or multiple are found. This is a runtime-safe equivalent of
// contextutil.DiscoverContextFromFiles (which imports protogen).
func discoverContextDescriptor(files *protoregistry.Files) (protoreflect.MessageDescriptor, error) {
	var found []protoreflect.MessageDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Messages().Len(); i++ {
			msg := fd.Messages().Get(i)
			if hasContextOption(msg.Options()) {
				found = append(found, msg)
			}
		}
		return true
	})

	if len(found) == 0 {
		return nil, fmt.Errorf("no message with (pbflags.context) option found in descriptors")
	}
	if len(found) > 1 {
		names := make([]string, len(found))
		for i, m := range found {
			names[i] = string(m.FullName())
		}
		return nil, fmt.Errorf("multiple messages annotated with (pbflags.context): %s", strings.Join(names, ", "))
	}
	return found[0], nil
}

// hasContextOption checks if the given message options contain the
// (pbflags.context) extension (field number 51003).
func hasContextOption(opts protoreflect.ProtoMessage) bool {
	if opts == nil {
		return false
	}
	rm := opts.ProtoReflect()

	// Try resolved extensions first.
	var found bool
	rm.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if fd.Number() == contextExtNum && fd.IsExtension() {
			found = true
			return false
		}
		return true
	})
	if found {
		return true
	}

	// Fall back to unknown fields (unresolved extensions).
	return hasContextInUnknown(rm.GetUnknown())
}

// hasContextInUnknown parses (pbflags.context) from unknown wire fields.
func hasContextInUnknown(b []byte) bool {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		if num == contextExtNum && typ == protowire.BytesType {
			_, n := protowire.ConsumeBytes(b)
			return n >= 0
		}
		n = consumeFieldValue(b, typ)
		if n < 0 {
			return false
		}
		b = b[n:]
	}
	return false
}

// consumeFieldValue skips a single wire field value.
func consumeFieldValue(b []byte, typ protowire.Type) int {
	switch typ {
	case protowire.VarintType:
		_, n := protowire.ConsumeVarint(b)
		return n
	case protowire.Fixed32Type:
		_, n := protowire.ConsumeFixed32(b)
		return n
	case protowire.Fixed64Type:
		_, n := protowire.ConsumeFixed64(b)
		return n
	case protowire.BytesType:
		_, n := protowire.ConsumeBytes(b)
		return n
	default:
		return -1
	}
}

// PruneContextDescriptorSet takes a full FileDescriptorSet, discovers the
// EvaluationContext message, and returns a minimal FileDescriptorSet containing
// only the EvaluationContext file and its transitive imports. This keeps the
// stored descriptor O(1) relative to the number of flags/features.
func PruneContextDescriptorSet(fullData []byte) ([]byte, error) {
	fds := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(fullData, fds); err != nil {
		return nil, fmt.Errorf("unmarshal descriptor set: %w", err)
	}

	files, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil, fmt.Errorf("build file registry: %w", err)
	}

	contextMsg, err := discoverContextDescriptor(files)
	if err != nil {
		return nil, err
	}

	// Collect the context message's file and all transitive imports.
	needed := map[string]bool{}
	collectImports(contextMsg.ParentFile(), needed)

	// Build pruned FileDescriptorSet with only needed files.
	pruned := &descriptorpb.FileDescriptorSet{}
	for _, fd := range fds.File {
		if needed[fd.GetName()] {
			pruned.File = append(pruned.File, fd)
		}
	}

	return proto.Marshal(pruned)
}

// collectImports recursively collects a file and all its imports.
func collectImports(fd protoreflect.FileDescriptor, needed map[string]bool) {
	name := string(fd.Path())
	if needed[name] {
		return
	}
	needed[name] = true
	for i := 0; i < fd.Imports().Len(); i++ {
		collectImports(fd.Imports().Get(i), needed)
	}
}

// LoadConditionEvaluatorFromDescriptorSet parses a (possibly pruned)
// FileDescriptorSet, discovers the EvaluationContext message, and returns a
// ready ConditionEvaluator. Returns (nil, nil) if data is nil or empty.
func LoadConditionEvaluatorFromDescriptorSet(data []byte, logger *slog.Logger) (*ConditionEvaluator, error) {
	if len(data) == 0 {
		return nil, nil
	}

	files, _, err := ParseDescriptorSet(data)
	if err != nil {
		return nil, fmt.Errorf("parse descriptor set: %w", err)
	}

	contextMsg, err := discoverContextDescriptor(files)
	if err != nil {
		return nil, err
	}

	return NewConditionEvaluator(contextMsg, logger)
}

// LoadContextDescriptorFromDB reads the pruned context descriptor set from the
// database. Returns (nil, nil) if no descriptor has been stored yet.
func LoadContextDescriptorFromDB(ctx context.Context, pool *pgxpool.Pool) ([]byte, error) {
	var data []byte
	err := pool.QueryRow(ctx,
		`SELECT descriptor_set FROM feature_flags.context_descriptor WHERE id = 1`,
	).Scan(&data)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query context descriptor: %w", err)
	}
	return data, nil
}

// UpsertContextDescriptor writes the pruned context descriptor set to the DB.
func UpsertContextDescriptor(ctx context.Context, tx pgx.Tx, descriptorSet []byte) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO feature_flags.context_descriptor (id, descriptor_set, updated_at)
		 VALUES (1, $1, now())
		 ON CONFLICT (id) DO UPDATE SET descriptor_set = $1, updated_at = now()`,
		descriptorSet,
	)
	return err
}
