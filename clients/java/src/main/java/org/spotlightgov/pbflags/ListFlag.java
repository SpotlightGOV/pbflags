package org.spotlightgov.pbflags;

import java.util.List;

/**
 * A typed list-valued feature flag. Evaluation never throws — all exceptions are caught internally,
 * logged, and the compiled default is returned.
 *
 * @param <E> the list element type (Boolean, String, Long, or Double)
 */
public interface ListFlag<E> {
  /** Evaluate globally (no entity context). */
  List<E> get();

  /** Evaluate with entity context (for layer-based flags). */
  List<E> get(String entityId);
}
