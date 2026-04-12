// Package pbflags provides core types for the pbflags evaluation context.
package pbflags

import "google.golang.org/protobuf/reflect/protoreflect"

// Dimension represents a single key-value pair in an evaluation context.
// Implementations set a named field on the EvaluationContext proto message
// via protoreflect. Generated dimension constructors (in the dims package)
// return Dimension values; application code should not implement this
// interface directly.
type Dimension interface {
	// Apply sets this dimension's value on the given proto message.
	// The message must be the EvaluationContext type. Apply silently
	// does nothing if the named field does not exist on the message.
	Apply(msg protoreflect.Message)
}

// StringDimension creates a Dimension that sets a string field.
func StringDimension(name protoreflect.Name, val string) Dimension {
	return stringDim{name: name, val: val}
}

// EnumDimension creates a Dimension that sets an enum field by ordinal.
func EnumDimension(name protoreflect.Name, val protoreflect.EnumNumber) Dimension {
	return enumDim{name: name, val: val}
}

// BoolDimension creates a Dimension that sets a bool field.
func BoolDimension(name protoreflect.Name, val bool) Dimension {
	return boolDim{name: name, val: val}
}

// Int64Dimension creates a Dimension that sets an int64 field.
func Int64Dimension(name protoreflect.Name, val int64) Dimension {
	return int64Dim{name: name, val: val}
}

type stringDim struct {
	name protoreflect.Name
	val  string
}

func (d stringDim) Apply(msg protoreflect.Message) {
	fd := msg.Descriptor().Fields().ByName(d.name)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfString(d.val))
}

type enumDim struct {
	name protoreflect.Name
	val  protoreflect.EnumNumber
}

func (d enumDim) Apply(msg protoreflect.Message) {
	fd := msg.Descriptor().Fields().ByName(d.name)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfEnum(d.val))
}

type boolDim struct {
	name protoreflect.Name
	val  bool
}

func (d boolDim) Apply(msg protoreflect.Message) {
	fd := msg.Descriptor().Fields().ByName(d.name)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfBool(d.val))
}

type int64Dim struct {
	name protoreflect.Name
	val  int64
}

func (d int64Dim) Apply(msg protoreflect.Message) {
	fd := msg.Descriptor().Fields().ByName(d.name)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfInt64(d.val))
}
