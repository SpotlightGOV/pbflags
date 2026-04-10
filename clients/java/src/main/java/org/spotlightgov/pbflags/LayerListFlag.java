package org.spotlightgov.pbflags;

import java.util.List;

/**
 * A typed list-valued feature flag that supports layer-scoped evaluation. Evaluation never throws —
 * all exceptions are caught internally, logged, and the compiled default is returned.
 *
 * @param <E> the list element type (Boolean, String, Long, or Double)
 * @param <ID> the layer ID type (e.g., UserID, EntityID)
 */
public interface LayerListFlag<E, ID> {
  /** Evaluate globally (no entity context). */
  List<E> get();

  /** Evaluate with layer-scoped entity context. */
  List<E> get(ID id);
}
