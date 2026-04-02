package org.spotlightgov.pbflags.generated;

import org.spotlightgov.pbflags.Flag;
import org.spotlightgov.pbflags.FlagEvaluator;

/**
 * Generated type-safe flag accessors for the {@code notifications} feature.
 *
 * <p>Controls notification delivery behavior
 */
public interface NotificationsFlags {

  String FEATURE_ID = "notifications";

  String EMAIL_ENABLED_ID = "notifications/1";
  String DIGEST_FREQUENCY_ID = "notifications/2";
  String MAX_RETRIES_ID = "notifications/3";
  String SCORE_THRESHOLD_ID = "notifications/4";

  boolean EMAIL_ENABLED_DEFAULT = true;
  String DIGEST_FREQUENCY_DEFAULT = "daily";
  long MAX_RETRIES_DEFAULT = 3L;
  double SCORE_THRESHOLD_DEFAULT = 0.75;

  /** Enable email notifications */
  Flag<Boolean> emailEnabled();

  /** How often to send digest emails */
  Flag<String> digestFrequency();

  /** Maximum retry attempts */
  Flag<Long> maxRetries();

  /** Score threshold for notification triggers */
  Flag<Double> scoreThreshold();

  /**
   * Creates an instance backed by a {@link FlagEvaluator}.
   */
  static NotificationsFlags forEvaluator(FlagEvaluator evaluator) {
    return new NotificationsFlags() {

      @Override
      public Flag<Boolean> emailEnabled() {
        return evaluator.flag(EMAIL_ENABLED_ID, Boolean.class, EMAIL_ENABLED_DEFAULT);
      }

      @Override
      public Flag<String> digestFrequency() {
        return evaluator.flag(DIGEST_FREQUENCY_ID, String.class, DIGEST_FREQUENCY_DEFAULT);
      }

      @Override
      public Flag<Long> maxRetries() {
        return evaluator.flag(MAX_RETRIES_ID, Long.class, MAX_RETRIES_DEFAULT);
      }

      @Override
      public Flag<Double> scoreThreshold() {
        return evaluator.flag(SCORE_THRESHOLD_ID, Double.class, SCORE_THRESHOLD_DEFAULT);
      }
    };
  }
}
