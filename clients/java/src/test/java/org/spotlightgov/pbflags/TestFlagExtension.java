package org.spotlightgov.pbflags;

import org.junit.jupiter.api.extension.AfterEachCallback;
import org.junit.jupiter.api.extension.BeforeAllCallback;
import org.junit.jupiter.api.extension.ExtensionContext;

/**
 * JUnit 5 extension providing an in-memory {@link FlagEvaluator} for tests.
 *
 * <p>No database, no cache, no migrations. Flags return compiled defaults unless explicitly
 * overridden via {@link #set}, {@link #setForEntity}, or {@link #kill}.
 *
 * <p>All overrides are reset after each test.
 *
 * <p>Usage:
 *
 * <pre>{@code
 * class MyTest {
 *   @RegisterExtension
 *   static final TestFlagExtension flags = new TestFlagExtension();
 *
 *   @Test
 *   void testOverride() {
 *     flags.set("notifications/1", false);
 *     var flags = NotificationsFlags.forEvaluator(flags.evaluator());
 *     assertFalse(flags.emailEnabled().get());
 *   }
 * }
 * }</pre>
 */
public final class TestFlagExtension implements BeforeAllCallback, AfterEachCallback {

  private final InMemoryFlagEvaluator evaluator = new InMemoryFlagEvaluator();

  public TestFlagExtension() {}

  @Override
  public void beforeAll(ExtensionContext context) {
    // No-op
  }

  @Override
  public void afterEach(ExtensionContext context) {
    evaluator.reset();
  }

  /** Returns the in-memory evaluator. Pass to {@code *Flags.forEvaluator()} in tests. */
  public FlagEvaluator evaluator() {
    return evaluator;
  }

  /** Set a global flag value. Immediately visible to the evaluator. */
  public void set(String flagId, Object value) {
    evaluator.set(flagId, value);
  }

  /** Set a per-entity flag value. */
  public void setForEntity(String flagId, String entityId, Object value) {
    evaluator.setForEntity(flagId, entityId, value);
  }

  /** Kill a flag globally (forces compiled default). */
  public void kill(String flagId) {
    evaluator.kill(flagId);
  }

  /** Reset all overrides and kills. Called automatically after each test. */
  public void reset() {
    evaluator.reset();
  }
}
