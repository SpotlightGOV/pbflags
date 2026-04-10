package org.spotlightgov.pbflags;

import org.spotlightgov.pbflags.v1.proto.EvaluateRequest;
import org.spotlightgov.pbflags.v1.proto.EvaluateResponse;
import org.spotlightgov.pbflags.v1.proto.EvaluatorStatus;
import org.spotlightgov.pbflags.v1.proto.FlagEvaluatorServiceGrpc;
import org.spotlightgov.pbflags.v1.proto.FlagValue;
import org.spotlightgov.pbflags.v1.proto.HealthRequest;
import org.spotlightgov.pbflags.v1.proto.HealthResponse;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.StatusRuntimeException;
import java.util.List;
import java.util.concurrent.TimeUnit;
import javax.annotation.Nullable;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * Thin gRPC client to a pbflags evaluator. Implements {@link FlagEvaluator} so it can be injected
 * wherever flag evaluation is needed.
 *
 * <p>All errors are caught and the compiled default is returned, maintaining the never-throw
 * guarantee.
 */
public final class FlagEvaluatorClient implements FlagEvaluator {

  private static final Logger logger = LoggerFactory.getLogger(FlagEvaluatorClient.class);

  private static final long EVALUATE_DEADLINE_MS = 2000;
  private static final long HEALTH_DEADLINE_MS = 500;

  private final FlagEvaluatorServiceGrpc.FlagEvaluatorServiceBlockingStub stub;
  private final ManagedChannel channel;

  public FlagEvaluatorClient(String target) {
    this.channel =
        ManagedChannelBuilder.forTarget(target)
            .usePlaintext()
            .maxInboundMessageSize(4 * 1024 * 1024)
            .build();
    this.stub = FlagEvaluatorServiceGrpc.newBlockingStub(channel);
    logger.info("FlagEvaluatorClient connecting to {}", target);
  }

  /**
   * Creates a client backed by the given channel. Use this for custom channel configuration (TLS,
   * interceptors, load balancing) or testing with {@code InProcessChannelBuilder}.
   *
   * <p>The caller is responsible for shutting down the channel, or may call {@link #shutdown()}
   * which will shut down the provided channel.
   */
  public static FlagEvaluatorClient forChannel(ManagedChannel channel) {
    return new FlagEvaluatorClient(
        channel, FlagEvaluatorServiceGrpc.newBlockingStub(channel));
  }

  FlagEvaluatorClient(
      ManagedChannel channel, FlagEvaluatorServiceGrpc.FlagEvaluatorServiceBlockingStub stub) {
    this.channel = channel;
    this.stub = stub;
  }

  @Override
  public <T> T evaluate(
      String flagId, Class<T> type, T compiledDefault, @Nullable String entityId) {
    try {
      EvaluateRequest.Builder req = EvaluateRequest.newBuilder().setFlagId(flagId);
      if (entityId != null && !entityId.isEmpty()) {
        req.setEntityId(entityId);
      }

      EvaluateResponse resp =
          stub.withDeadlineAfter(EVALUATE_DEADLINE_MS, TimeUnit.MILLISECONDS).evaluate(req.build());

      if (!resp.hasValue()) {
        return compiledDefault;
      }

      return extractValue(resp.getValue(), type, compiledDefault);
    } catch (StatusRuntimeException e) {
      logger.error("Evaluator call failed for flag {}: {}", flagId, e.getStatus(), e);
      return compiledDefault;
    } catch (Exception e) {
      logger.error("Evaluator call failed for flag {}", flagId, e);
      return compiledDefault;
    }
  }

  /** Check evaluator health. Returns {@code true} if the evaluator is SERVING. */
  public boolean isHealthy() {
    try {
      HealthResponse resp =
          stub.withDeadlineAfter(HEALTH_DEADLINE_MS, TimeUnit.MILLISECONDS)
              .health(HealthRequest.getDefaultInstance());
      return resp.getStatus() == EvaluatorStatus.EVALUATOR_STATUS_SERVING;
    } catch (Exception e) {
      logger.warn("Evaluator health check failed", e);
      return false;
    }
  }

  /** Shut down the gRPC channel. Call on server shutdown. */
  public void shutdown() {
    channel.shutdown();
    try {
      if (!channel.awaitTermination(5, TimeUnit.SECONDS)) {
        channel.shutdownNow();
      }
    } catch (InterruptedException e) {
      channel.shutdownNow();
      Thread.currentThread().interrupt();
    }
  }

  @Override
  public <E> List<E> evaluateList(
      String flagId, Class<E> elementType, List<E> compiledDefault, @Nullable String entityId) {
    try {
      EvaluateRequest.Builder req = EvaluateRequest.newBuilder().setFlagId(flagId);
      if (entityId != null && !entityId.isEmpty()) {
        req.setEntityId(entityId);
      }

      EvaluateResponse resp =
          stub.withDeadlineAfter(EVALUATE_DEADLINE_MS, TimeUnit.MILLISECONDS).evaluate(req.build());

      if (!resp.hasValue()) {
        return compiledDefault;
      }

      return extractListValue(resp.getValue(), elementType, compiledDefault);
    } catch (StatusRuntimeException e) {
      logger.error("Evaluator call failed for list flag {}: {}", flagId, e.getStatus(), e);
      return compiledDefault;
    } catch (Exception e) {
      logger.error("Evaluator call failed for list flag {}", flagId, e);
      return compiledDefault;
    }
  }

  @SuppressWarnings("unchecked")
  private static <T> T extractValue(FlagValue value, Class<T> type, T fallback) {
    try {
      if (type == Boolean.class) {
        return value.getValueCase() == FlagValue.ValueCase.BOOL_VALUE
            ? (T) Boolean.valueOf(value.getBoolValue())
            : fallback;
      } else if (type == String.class) {
        return value.getValueCase() == FlagValue.ValueCase.STRING_VALUE
            ? (T) value.getStringValue()
            : fallback;
      } else if (type == Long.class) {
        return value.getValueCase() == FlagValue.ValueCase.INT64_VALUE
            ? (T) Long.valueOf(value.getInt64Value())
            : fallback;
      } else if (type == Integer.class) {
        return value.getValueCase() == FlagValue.ValueCase.INT64_VALUE
            ? (T) Integer.valueOf((int) value.getInt64Value())
            : fallback;
      } else if (type == Double.class) {
        return value.getValueCase() == FlagValue.ValueCase.DOUBLE_VALUE
            ? (T) Double.valueOf(value.getDoubleValue())
            : fallback;
      } else if (type == Float.class) {
        return value.getValueCase() == FlagValue.ValueCase.DOUBLE_VALUE
            ? (T) Float.valueOf((float) value.getDoubleValue())
            : fallback;
      }
    } catch (Exception e) {
      logger.error("Failed to extract flag value as {}", type.getSimpleName(), e);
    }
    return fallback;
  }

  @SuppressWarnings("unchecked")
  private static <E> List<E> extractListValue(
      FlagValue value, Class<E> elementType, List<E> fallback) {
    try {
      if (elementType == Boolean.class
          && value.getValueCase() == FlagValue.ValueCase.BOOL_LIST_VALUE) {
        return (List<E>) List.copyOf(value.getBoolListValue().getValuesList());
      } else if (elementType == String.class
          && value.getValueCase() == FlagValue.ValueCase.STRING_LIST_VALUE) {
        return (List<E>) List.copyOf(value.getStringListValue().getValuesList());
      } else if (elementType == Long.class
          && value.getValueCase() == FlagValue.ValueCase.INT64_LIST_VALUE) {
        return (List<E>) List.copyOf(value.getInt64ListValue().getValuesList());
      } else if (elementType == Double.class
          && value.getValueCase() == FlagValue.ValueCase.DOUBLE_LIST_VALUE) {
        return (List<E>) List.copyOf(value.getDoubleListValue().getValuesList());
      }
    } catch (Exception e) {
      logger.error("Failed to extract list flag value as List<{}>", elementType.getSimpleName(), e);
    }
    return fallback;
  }
}
