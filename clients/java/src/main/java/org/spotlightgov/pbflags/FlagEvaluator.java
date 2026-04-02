package org.spotlightgov.pbflags;

import javax.annotation.Nullable;

/**
 * Evaluates feature flags with a never-throw guarantee.
 *
 * <p>Evaluation precedence (for the production implementation):
 *
 * <ol>
 *   <li>Global KILLED → compiled default
 *   <li>Override KILLED or DEFAULT → compiled default
 *   <li>Override ENABLED → override value
 *   <li>Global DEFAULT → compiled default
 *   <li>Global ENABLED → configured value (fallback to compiled default if null)
 * </ol>
 *
 * <p>All exceptions are caught, logged, counted, and the compiled default is returned. The
 * application must never crash due to a flag evaluation.
 */
public interface FlagEvaluator {

  /**
   * Evaluate a flag globally (no entity context).
   *
   * @param flagId the flag identifier (e.g., "notifications/1")
   * @param type the expected value type
   * @param compiledDefault the compiled default from the proto definition
   * @return the resolved value, never null for known flags
   */
  default <T> T evaluate(String flagId, Class<T> type, T compiledDefault) {
    return evaluate(flagId, type, compiledDefault, null);
  }

  /**
   * Evaluate a flag with optional entity context.
   *
   * @param flagId the flag identifier
   * @param type the expected value type
   * @param compiledDefault the compiled default from the proto definition
   * @param entityId the entity context for layer-based flags, or null for global-only
   * @return the resolved value, never null for known flags
   */
  <T> T evaluate(String flagId, Class<T> type, T compiledDefault, @Nullable String entityId);

  /**
   * Creates a {@link Flag} instance for the given flag ID and compiled default.
   *
   * <p>The returned object is lightweight and stateless — it delegates to this evaluator on each
   * {@code get()} call. Safe to cache as a singleton.
   */
  default <T> Flag<T> flag(String flagId, Class<T> type, T compiledDefault) {
    return new Flag<>() {
      @Override
      public T get() {
        return evaluate(flagId, type, compiledDefault, null);
      }

      @Override
      public T get(String entityId) {
        return evaluate(flagId, type, compiledDefault, entityId);
      }
    };
  }
}
