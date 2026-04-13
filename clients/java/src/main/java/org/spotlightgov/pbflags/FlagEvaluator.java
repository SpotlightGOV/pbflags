package org.spotlightgov.pbflags;

import java.util.List;

/**
 * Evaluates feature flags with a never-throw guarantee.
 *
 * <p>Context dimensions (user ID, plan tier, etc.) are bound at the evaluator level via {@link
 * #with(Dimension...)}. The returned evaluator carries the bound context for all subsequent
 * evaluations. This replaces the legacy per-entity override model.
 *
 * <p>All exceptions are caught, logged, counted, and the compiled default is returned. The
 * application must never crash due to a flag evaluation.
 */
public interface FlagEvaluator {

  /**
   * Returns a new evaluator with additional context dimensions bound. Dimensions from the parent
   * are preserved; new dimensions are appended.
   *
   * @param dims the dimensions to bind
   * @return a new evaluator with accumulated dimensions
   */
  default FlagEvaluator with(Dimension... dims) {
    throw new UnsupportedOperationException("This evaluator does not support context dimensions");
  }

  /**
   * Evaluate a flag against the bound context.
   *
   * @param flagId the flag identifier (e.g., "notifications/1")
   * @param type the expected value type
   * @param compiledDefault the compiled default from the proto definition
   * @return the resolved value, never null for known flags
   */
  <T> T evaluate(String flagId, Class<T> type, T compiledDefault);

  /**
   * Creates a {@link Flag} instance for the given flag ID and compiled default.
   *
   * <p>The returned object is lightweight and stateless — it delegates to this evaluator on each
   * {@code get()} call. Safe to cache as a singleton.
   */
  default <T> Flag<T> flag(String flagId, Class<T> type, T compiledDefault) {
    return () -> evaluate(flagId, type, compiledDefault);
  }

  /**
   * Evaluate a list-valued flag against the bound context.
   *
   * @param flagId the flag identifier
   * @param elementType the expected element type (e.g., String.class)
   * @param compiledDefault the compiled default from the proto definition
   * @return the resolved list, never null
   */
  <E> List<E> evaluateList(String flagId, Class<E> elementType, List<E> compiledDefault);

  /**
   * Creates a {@link ListFlag} instance for a list-valued flag.
   *
   * <p>The returned object is lightweight and stateless — it delegates to this evaluator on each
   * {@code get()} call. Safe to cache as a singleton.
   */
  default <E> ListFlag<E> listFlag(String flagId, Class<E> elementType, List<E> compiledDefault) {
    return () -> evaluateList(flagId, elementType, compiledDefault);
  }
}
