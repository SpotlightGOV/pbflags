package org.spotlightgov.pbflags;

import java.util.List;

/**
 * A typed list-valued feature flag. Evaluation never throws — all exceptions are caught internally,
 * logged, and the compiled default is returned.
 *
 * <p>Context dimensions are bound at the evaluator level via {@link FlagEvaluator#with(Dimension...
 * dims)}, not on individual flag calls.
 *
 * @param <E> the list element type (Boolean, String, Long, or Double)
 */
@FunctionalInterface
public interface ListFlag<E> {
  /** Evaluate the flag against the evaluator's bound context. */
  List<E> get();
}
