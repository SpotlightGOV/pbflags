package org.spotlightgov.pbflags;

/**
 * A typed feature flag. Evaluation never throws — all exceptions are caught internally, logged, and
 * the compiled default is returned.
 *
 * <p>Context dimensions are bound at the evaluator level via {@link FlagEvaluator#with(Dimension...
 * dims)}, not on individual flag calls.
 *
 * @param <T> the flag value type (Boolean, String, Long, or Double)
 */
@FunctionalInterface
public interface Flag<T> {
  /** Evaluate the flag against the evaluator's bound context. */
  T get();
}
