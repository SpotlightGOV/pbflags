package org.spotlightgov.pbflags;

/**
 * A typed feature flag that supports layer-scoped evaluation.
 * Evaluation never throws — all exceptions are caught internally, logged, and
 * the compiled default is returned.
 *
 * @param <T> the flag value type (Boolean, String, Long, or Double)
 * @param <ID> the layer ID type (e.g., UserID, EntityID)
 */
public interface LayerFlag<T, ID> {
  /** Evaluate globally (no entity context). */
  T get();

  /** Evaluate with layer-scoped entity context. */
  T get(ID id);
}
