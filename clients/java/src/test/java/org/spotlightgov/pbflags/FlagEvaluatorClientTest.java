package org.spotlightgov.pbflags;

import static org.junit.jupiter.api.Assertions.*;

import io.grpc.ManagedChannel;
import io.grpc.Server;
import io.grpc.Status;
import io.grpc.inprocess.InProcessChannelBuilder;
import io.grpc.inprocess.InProcessServerBuilder;
import io.grpc.stub.StreamObserver;
import java.io.IOException;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.spotlightgov.pbflags.v1.proto.EvaluateRequest;
import org.spotlightgov.pbflags.v1.proto.EvaluateResponse;
import org.spotlightgov.pbflags.v1.proto.EvaluatorStatus;
import org.spotlightgov.pbflags.v1.proto.FlagEvaluatorServiceGrpc;
import org.spotlightgov.pbflags.v1.proto.FlagValue;
import org.spotlightgov.pbflags.v1.proto.HealthRequest;
import org.spotlightgov.pbflags.v1.proto.HealthResponse;

class FlagEvaluatorClientTest {

  private Server server;
  private ManagedChannel channel;
  private FlagEvaluatorClient client;
  private TestEvaluatorService service;

  @BeforeEach
  void setUp() throws IOException {
    service = new TestEvaluatorService();
    String serverName = InProcessServerBuilder.generateName();
    server =
        InProcessServerBuilder.forName(serverName).directExecutor().addService(service).build();
    server.start();
    channel = InProcessChannelBuilder.forName(serverName).directExecutor().build();
    client = FlagEvaluatorClient.forChannel(channel);
  }

  @AfterEach
  void tearDown() {
    client.shutdown();
    server.shutdownNow();
  }

  // --- Type extraction tests ---

  @Test
  void evaluateBool() {
    service.nextValue = FlagValue.newBuilder().setBoolValue(true).build();
    boolean result = client.evaluate("test/1", Boolean.class, false);
    assertTrue(result);
  }

  @Test
  void evaluateBoolFalse() {
    service.nextValue = FlagValue.newBuilder().setBoolValue(false).build();
    boolean result = client.evaluate("test/1", Boolean.class, true);
    assertFalse(result);
  }

  @Test
  void evaluateString() {
    service.nextValue = FlagValue.newBuilder().setStringValue("weekly").build();
    String result = client.evaluate("test/2", String.class, "daily");
    assertEquals("weekly", result);
  }

  @Test
  void evaluateLong() {
    service.nextValue = FlagValue.newBuilder().setInt64Value(42).build();
    long result = client.evaluate("test/3", Long.class, 0L);
    assertEquals(42L, result);
  }

  @Test
  void evaluateDouble() {
    service.nextValue = FlagValue.newBuilder().setDoubleValue(3.14).build();
    double result = client.evaluate("test/4", Double.class, 0.0);
    assertEquals(3.14, result, 0.001);
  }

  @Test
  void evaluateInteger() {
    service.nextValue = FlagValue.newBuilder().setInt64Value(7).build();
    int result = client.evaluate("test/5", Integer.class, 0);
    assertEquals(7, result);
  }

  @Test
  void evaluateFloat() {
    service.nextValue = FlagValue.newBuilder().setDoubleValue(1.5).build();
    float result = client.evaluate("test/6", Float.class, 0.0f);
    assertEquals(1.5f, result, 0.001f);
  }

  // --- Context propagation ---

  @Test
  void noContextByDefault() {
    service.nextValue = FlagValue.newBuilder().setBoolValue(true).build();
    client.evaluate("test/1", Boolean.class, false);
    assertFalse(service.lastRequest.hasContext());
  }

  @Test
  void withoutContextPrototypeThrows() {
    assertThrows(
        IllegalStateException.class, () -> client.with(Dimension.ofString("user_id", "user-42")));
  }

  // --- Compiled default fallback ---

  @Test
  void noValueReturnsCompiledDefault() {
    service.nextValue = null; // response with no value
    boolean result = client.evaluate("test/1", Boolean.class, true);
    assertTrue(result);
  }

  @Test
  void typeMismatchReturnsCompiledDefault() {
    // Return a string value but ask for Boolean
    service.nextValue = FlagValue.newBuilder().setStringValue("oops").build();
    boolean result = client.evaluate("test/1", Boolean.class, true);
    assertTrue(result);
  }

  // --- Never-throw guarantee (gRPC errors) ---

  @Test
  void grpcErrorReturnsCompiledDefaultBool() {
    service.nextError = Status.UNAVAILABLE;
    boolean result = client.evaluate("test/1", Boolean.class, true);
    assertTrue(result);
  }

  @Test
  void grpcErrorReturnsCompiledDefaultString() {
    service.nextError = Status.UNAVAILABLE;
    String result = client.evaluate("test/2", String.class, "fallback");
    assertEquals("fallback", result);
  }

  @Test
  void grpcErrorReturnsCompiledDefaultLong() {
    service.nextError = Status.INTERNAL;
    long result = client.evaluate("test/3", Long.class, 99L);
    assertEquals(99L, result);
  }

  @Test
  void grpcErrorReturnsCompiledDefaultDouble() {
    service.nextError = Status.DEADLINE_EXCEEDED;
    double result = client.evaluate("test/4", Double.class, 1.23);
    assertEquals(1.23, result, 0.001);
  }

  // --- Health checks ---

  @Test
  void isHealthyWhenServing() {
    service.healthStatus = EvaluatorStatus.EVALUATOR_STATUS_SERVING;
    assertTrue(client.isHealthy());
  }

  @Test
  void isNotHealthyWhenDegraded() {
    service.healthStatus = EvaluatorStatus.EVALUATOR_STATUS_DEGRADED;
    assertFalse(client.isHealthy());
  }

  @Test
  void isNotHealthyWhenConnecting() {
    service.healthStatus = EvaluatorStatus.EVALUATOR_STATUS_CONNECTING;
    assertFalse(client.isHealthy());
  }

  @Test
  void isNotHealthyOnError() {
    service.healthError = Status.UNAVAILABLE;
    assertFalse(client.isHealthy());
  }

  // --- Test service implementation ---

  static class TestEvaluatorService extends FlagEvaluatorServiceGrpc.FlagEvaluatorServiceImplBase {

    volatile FlagValue nextValue;
    volatile Status nextError;
    volatile EvaluateRequest lastRequest;
    volatile EvaluatorStatus healthStatus = EvaluatorStatus.EVALUATOR_STATUS_SERVING;
    volatile Status healthError;

    @Override
    public void evaluate(EvaluateRequest request, StreamObserver<EvaluateResponse> observer) {
      lastRequest = request;

      if (nextError != null) {
        observer.onError(nextError.asRuntimeException());
        nextError = null;
        return;
      }

      EvaluateResponse.Builder resp = EvaluateResponse.newBuilder().setFlagId(request.getFlagId());
      if (nextValue != null) {
        resp.setValue(nextValue);
      }
      observer.onNext(resp.build());
      observer.onCompleted();
    }

    @Override
    public void health(HealthRequest request, StreamObserver<HealthResponse> observer) {
      if (healthError != null) {
        observer.onError(healthError.asRuntimeException());
        healthError = null;
        return;
      }
      observer.onNext(HealthResponse.newBuilder().setStatus(healthStatus).build());
      observer.onCompleted();
    }
  }
}
