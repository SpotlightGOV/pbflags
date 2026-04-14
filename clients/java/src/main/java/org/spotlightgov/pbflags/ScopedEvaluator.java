package org.spotlightgov.pbflags;

/**
 * A context-scoped evaluator that binds dimensions to a {@link FlagEvaluator} and makes it
 * available via thread-local storage for the duration of a request.
 *
 * <p>Typical usage in HTTP middleware:
 *
 * <pre>{@code
 * try (var scope = ScopedEvaluator.bind(evaluator,
 *     Dimensions.userId(request.getUserId()),
 *     Dimensions.plan(request.getPlan()))) {
 *   // Flag accessors created via ScopedEvaluator.current() see the bound dimensions.
 *   chain.doFilter(request, response);
 * }
 * }</pre>
 *
 * <p>Retrieving the scoped evaluator downstream:
 *
 * <pre>{@code
 * FlagEvaluator eval = ScopedEvaluator.current();
 * var flags = NotificationsFlags.forEvaluator(eval);
 * }</pre>
 */
public final class ScopedEvaluator {

  private static final ThreadLocal<FlagEvaluator> CURRENT = new ThreadLocal<>();

  private ScopedEvaluator() {}

  /**
   * Binds a {@link FlagEvaluator} with the given dimensions to the current thread. Returns an
   * {@link AutoCloseable} scope that restores the previous evaluator on close.
   *
   * @param evaluator the base evaluator
   * @param dims dimensions to bind (e.g., user ID, plan tier)
   * @return a scope that must be closed when the request completes
   */
  public static Scope bind(FlagEvaluator evaluator, Dimension... dims) {
    FlagEvaluator previous = CURRENT.get();
    FlagEvaluator scoped = dims.length > 0 ? evaluator.with(dims) : evaluator;
    CURRENT.set(scoped);
    return () -> {
      if (previous == null) {
        CURRENT.remove();
      } else {
        CURRENT.set(previous);
      }
    };
  }

  /**
   * Returns the evaluator bound to the current thread, or throws if none is bound.
   *
   * @throws IllegalStateException if no evaluator is bound to the current thread
   */
  public static FlagEvaluator current() {
    FlagEvaluator eval = CURRENT.get();
    if (eval == null) {
      throw new IllegalStateException(
          "No ScopedEvaluator bound to current thread. "
              + "Call ScopedEvaluator.bind() in request middleware.");
    }
    return eval;
  }

  /**
   * Returns the evaluator bound to the current thread, or the given fallback if none is bound.
   *
   * @param fallback evaluator to use when no scope is active
   */
  public static FlagEvaluator currentOr(FlagEvaluator fallback) {
    FlagEvaluator eval = CURRENT.get();
    return eval != null ? eval : fallback;
  }

  /** An {@link AutoCloseable} scope that restores the previous evaluator on close. */
  @FunctionalInterface
  public interface Scope extends AutoCloseable {
    @Override
    void close(); // no checked exception
  }
}
