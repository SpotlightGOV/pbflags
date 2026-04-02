package io.pbflags;

/**
 * A typed feature flag. Evaluation never throws — all exceptions are caught internally, logged, and
 * the compiled default is returned.
 *
 * @param <T> the flag value type (Boolean, String, Long, or Double)
 */
public interface Flag<T> {
  /** Evaluate globally (no entity context). */
  T get();

  /** Evaluate with entity context (for layer-based flags). */
  T get(String entityId);
}
