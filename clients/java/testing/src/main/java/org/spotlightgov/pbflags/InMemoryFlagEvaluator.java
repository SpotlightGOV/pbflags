package org.spotlightgov.pbflags;

import java.util.List;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;

/**
 * In-memory flag evaluator for tests. No database, no cache, no metrics.
 *
 * <p>Flags return compiled defaults unless explicitly overridden via {@link #set} or killed via
 * {@link #kill}.
 */
public final class InMemoryFlagEvaluator implements FlagEvaluator {

  private final ConcurrentHashMap<String, String> globalOverrides = new ConcurrentHashMap<>();
  private final Set<String> killedFlags = ConcurrentHashMap.newKeySet();

  /** Set a global flag value. */
  public void set(String flagId, Object value) {
    killedFlags.remove(flagId);
    globalOverrides.put(flagId, String.valueOf(value));
  }

  /** Kill a flag globally (forces compiled default). */
  public void kill(String flagId) {
    globalOverrides.remove(flagId);
    killedFlags.add(flagId);
  }

  /** Reset all overrides and kills. */
  public void reset() {
    globalOverrides.clear();
    killedFlags.clear();
  }

  @Override
  @SuppressWarnings("unchecked")
  public <T> T evaluate(String flagId, Class<T> type, T compiledDefault) {
    if (killedFlags.contains(flagId)) {
      return compiledDefault;
    }

    String globalValue = globalOverrides.get(flagId);
    if (globalValue != null) {
      return parseValue(globalValue, type, compiledDefault);
    }

    return compiledDefault;
  }

  @Override
  public <E> List<E> evaluateList(String flagId, Class<E> elementType, List<E> compiledDefault) {
    if (killedFlags.contains(flagId)) {
      return compiledDefault;
    }
    return compiledDefault;
  }

  @SuppressWarnings("unchecked")
  private static <T> T parseValue(String raw, Class<T> type, T fallback) {
    try {
      if (type == Boolean.class) {
        return (T) Boolean.valueOf(raw);
      } else if (type == String.class) {
        return (T) raw;
      } else if (type == Long.class) {
        return (T) Long.valueOf(raw);
      } else if (type == Double.class) {
        return (T) Double.valueOf(raw);
      }
    } catch (NumberFormatException e) {
      return fallback;
    }
    return fallback;
  }
}
