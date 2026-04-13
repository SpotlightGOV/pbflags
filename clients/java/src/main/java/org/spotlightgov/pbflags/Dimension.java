package org.spotlightgov.pbflags;

import com.google.protobuf.Descriptors;
import com.google.protobuf.Message;

/**
 * A single key-value pair in an evaluation context. Dimensions set named fields on the
 * EvaluationContext proto message via reflection. Generated dimension constructors (in the Dims
 * class) return Dimension values; application code should not implement this interface directly.
 */
@FunctionalInterface
public interface Dimension {

  /** Sets this dimension's value on the given proto builder. */
  void apply(Message.Builder builder);

  /** Creates a Dimension that sets a string field by proto field name. */
  static Dimension ofString(String fieldName, String value) {
    return builder -> {
      Descriptors.FieldDescriptor fd = builder.getDescriptorForType().findFieldByName(fieldName);
      if (fd != null && fd.getType() == Descriptors.FieldDescriptor.Type.STRING) {
        builder.setField(fd, value);
      }
    };
  }

  /** Creates a Dimension that sets a bool field by proto field name. */
  static Dimension ofBool(String fieldName, boolean value) {
    return builder -> {
      Descriptors.FieldDescriptor fd = builder.getDescriptorForType().findFieldByName(fieldName);
      if (fd != null && fd.getType() == Descriptors.FieldDescriptor.Type.BOOL) {
        builder.setField(fd, value);
      }
    };
  }

  /** Creates a Dimension that sets an int64 field by proto field name. */
  static Dimension ofInt64(String fieldName, long value) {
    return builder -> {
      Descriptors.FieldDescriptor fd = builder.getDescriptorForType().findFieldByName(fieldName);
      if (fd != null && fd.getType() == Descriptors.FieldDescriptor.Type.INT64) {
        builder.setField(fd, value);
      }
    };
  }

  /**
   * Creates a Dimension that sets an enum field by proto field name and enum value descriptor. Use
   * the generated Dims class for type-safe enum constructors.
   */
  static Dimension ofEnum(
      String fieldName, com.google.protobuf.Descriptors.EnumValueDescriptor value) {
    return builder -> {
      Descriptors.FieldDescriptor fd = builder.getDescriptorForType().findFieldByName(fieldName);
      if (fd != null && fd.getType() == Descriptors.FieldDescriptor.Type.ENUM) {
        builder.setField(fd, value);
      }
    };
  }
}
