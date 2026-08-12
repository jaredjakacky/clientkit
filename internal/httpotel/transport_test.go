package httpotel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	clientkitotel "github.com/jaredjakacky/clientkit/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestTransportCreatesOneClientSpanAndInjectsItsContextPerRoundTrip(t *testing.T) {
	traces := newTestTracerProvider()
	statuses := []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusNoContent}
	var traceHeaders []string
	base := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		traceHeaders = append(traceHeaders, request.Header.Get("Traceparent"))
		status := statuses[len(traceHeaders)-1]
		return &http.Response{
			StatusCode: status,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	})
	transport, err := NewTransport(base, Config{
		TracerProvider: traces,
		Inject: func(ctx context.Context, headers http.Header) {
			propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(headers))
		},
	})
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}

	logicalCtx, logical := traces.Tracer("test").Start(
		context.Background(),
		"logical",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	logicalCtx = WithOperation(logicalCtx, "payments", "payments.lookup")
	for attempt := 1; attempt <= len(statuses); attempt++ {
		ctx := WithExecutionAttempt(logicalCtx, attempt)
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/resource", nil)
		if requestErr != nil {
			t.Fatalf("NewRequestWithContext() error = %v", requestErr)
		}
		response, roundTripErr := transport.RoundTrip(request)
		if roundTripErr != nil {
			t.Fatalf("RoundTrip() attempt %d error = %v", attempt, roundTripErr)
		}
		_ = response.Body.Close()
	}
	logical.End()

	spans := traces.spans()
	if len(spans) != 4 {
		t.Fatalf("span count = %d, want logical plus three physical attempts", len(spans))
	}
	parent := spans[0]
	if parent.kind != trace.SpanKindInternal {
		t.Fatalf("logical span kind = %v, want INTERNAL", parent.kind)
	}
	for index, span := range spans[1:] {
		attempt := index + 1
		if span.kind != trace.SpanKindClient || span.name != http.MethodGet || span.endCount != 1 {
			t.Fatalf("attempt %d span = %#v, want ended GET CLIENT span", attempt, span)
		}
		if span.parentSpanID != parent.spanContext.SpanID() || span.spanContext.TraceID() != parent.spanContext.TraceID() {
			t.Fatalf("attempt %d parent/trace = (%s, %s), want logical (%s, %s)", attempt, span.parentSpanID, span.spanContext.TraceID(), parent.spanContext.SpanID(), parent.spanContext.TraceID())
		}
		if got := intAttribute(span.attributes, clientkitotel.AttributeAttemptNumber); got != int64(attempt) {
			t.Fatalf("attempt %d clientkit attempt attribute = %d", attempt, got)
		}
		if got := stringAttribute(span.attributes, clientkitotel.AttributeClientName); got != "payments" {
			t.Fatalf("attempt %d client name = %q", attempt, got)
		}
		resend, found := findAttribute(span.attributes, semconv.HTTPRequestResendCountKey)
		if attempt == 1 && found {
			t.Fatalf("first attempt unexpectedly has resend count %v", resend.Value)
		}
		if attempt > 1 && (!found || resend.Value.AsInt64() != int64(attempt-1)) {
			t.Fatalf("attempt %d resend count = (%v, %t), want %d", attempt, resend.Value, found, attempt-1)
		}

		carrier := propagation.HeaderCarrier(http.Header{"Traceparent": []string{traceHeaders[index]}})
		injected := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), carrier))
		if !injected.IsValid() || injected.SpanID() != span.spanContext.SpanID() {
			t.Fatalf("attempt %d injected span = %s, want %s", attempt, injected.SpanID(), span.spanContext.SpanID())
		}
	}
	if spans[1].status != codes.Error || spans[2].status != codes.Error || spans[3].status == codes.Error {
		t.Fatalf("attempt statuses = (%v, %v, %v), want error, error, non-error", spans[1].status, spans[2].status, spans[3].status)
	}
}

func TestTransportEndsSpanAtHeadersWithoutOwningResponseBody(t *testing.T) {
	traces := newTestTracerProvider()
	body := &trackedBody{Reader: strings.NewReader("payload")}
	transport, err := NewTransport(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			ProtoMajor: 2,
			Header:     make(http.Header),
			Body:       body,
			Request:    request,
		}, nil
	}), Config{TracerProvider: traces})
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	spans := traces.spans()
	if len(spans) != 1 || spans[0].endCount != 1 {
		t.Fatalf("spans after headers = %#v, want one ended span", spans)
	}
	if body.closed {
		t.Fatal("transport closed caller-owned response body")
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("response Body.Close() error = %v", err)
	}
}

func TestTransportTargetAttributesAreSafeAndOptIn(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
	}{
		{name: "safe defaults"},
		{name: "explicit target attributes", enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			traces := newTestTracerProvider()
			transport, err := NewTransport(roundTripperFunc(successResponse), Config{
				TracerProvider:          traces,
				RequestTargetAttributes: test.enabled,
			})
			if err != nil {
				t.Fatalf("NewTransport() error = %v", err)
			}
			request, _ := http.NewRequest("PURGE", "https://user:password@example.test:8443/accounts/123?token=secret&empty=", nil)
			if _, err := transport.RoundTrip(request); err != nil {
				t.Fatalf("RoundTrip() error = %v", err)
			}
			span := traces.spans()[0]
			if span.name != "HTTP" || stringAttribute(span.attributes, string(semconv.HTTPRequestMethodKey)) != "_OTHER" || stringAttribute(span.attributes, string(semconv.HTTPRequestMethodOriginalKey)) != "PURGE" {
				t.Fatalf("unknown method span = %#v, want HTTP/_OTHER plus original method", span)
			}
			serialized := attributesString(span.attributes)
			if strings.Contains(serialized, "password") || strings.Contains(serialized, "secret") {
				t.Fatalf("span attributes exposed credentials or query value: %s", serialized)
			}
			_, hasAddress := findAttribute(span.attributes, semconv.ServerAddressKey)
			_, hasPort := findAttribute(span.attributes, semconv.ServerPortKey)
			urlValue, hasURL := findAttribute(span.attributes, semconv.URLFullKey)
			if !test.enabled {
				if hasAddress || hasPort || hasURL || strings.Contains(serialized, "example.test") {
					t.Fatalf("default span attributes expose request target: %s", serialized)
				}
				return
			}
			if !hasAddress || stringAttribute(span.attributes, string(semconv.ServerAddressKey)) != "example.test" || !hasPort || intAttribute(span.attributes, string(semconv.ServerPortKey)) != 8443 || !hasURL {
				t.Fatalf("explicit target attributes = %s", serialized)
			}
			fullURL := urlValue.Value.AsString()
			if fullURL != "https://example.test:8443/accounts/123" {
				t.Fatalf("sanitized url.full = %q", fullURL)
			}
		})
	}
}

func TestTransportContainsInjectionPanicsAndPreservesCallerHeaders(t *testing.T) {
	transport, err := NewTransport(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Caller") != "preserved" || request.Header.Get("Partial") != "" {
			t.Fatalf("wire headers after injection panic = %#v", request.Header)
		}
		return successResponse(request)
	}), Config{Inject: func(_ context.Context, headers http.Header) {
		headers.Set("Partial", "must-be-removed")
		panic("inject")
	}})
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	request.Header.Set("Caller", "preserved")
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if request.Header.Get("Caller") != "preserved" || request.Header.Get("Partial") != "" {
		t.Fatalf("caller headers after RoundTrip = %#v", request.Header)
	}
}

func TestTransportRejectsLateResultAfterCancellation(t *testing.T) {
	traces := newTestTracerProvider()
	meters, metrics := newTestMeterProvider()
	started := make(chan struct{})
	release := make(chan struct{})
	body := &trackedBody{Reader: strings.NewReader("late")}
	transport, err := NewTransport(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
			Request:    request,
		}, nil
	}), Config{
		TracerProvider:  traces,
		MeterProvider:   meters,
		StandardMetrics: true,
	})
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/resource", nil)
	type result struct {
		response *http.Response
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		response, roundTripErr := transport.RoundTrip(request)
		resultCh <- result{response: response, err: roundTripErr}
	}()
	<-started
	cancel()
	close(release)
	got := <-resultCh
	if got.response != nil || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("RoundTrip() = (%v, %v), want context.Canceled", got.response, got.err)
	}
	if !body.closed {
		t.Fatal("late response body was not closed")
	}
	spans := traces.spans()
	if len(spans) != 1 || spans[0].status != codes.Error || stringAttribute(spans[0].attributes, string(semconv.ErrorTypeKey)) != "canceled" {
		t.Fatalf("spans = %#v, want one canceled error span", spans)
	}
	records := metrics.records()
	if len(records) != 1 || stringAttribute(records[0].attributes.ToSlice(), string(semconv.ErrorTypeKey)) != "canceled" {
		t.Fatalf("metric records = %#v, want canceled error.type", records)
	}
}

func TestTransportForwardsCloseIdleConnections(t *testing.T) {
	base := &idleClosingRoundTripper{}
	transport, err := NewTransport(base, Config{})
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	transport.CloseIdleConnections()
	if base.closeCalls != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", base.closeCalls)
	}
	var nilTransport *Transport
	nilTransport.CloseIdleConnections()
}

func TestTransportStandardMetricIsPerRoundTripAndSignalAttributesStaySeparate(t *testing.T) {
	traces := newTestTracerProvider()
	meters, metrics := newTestMeterProvider()
	transport, err := NewTransport(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			ProtoMajor: 2,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	}), Config{
		TracerProvider: traces,
		MeterProvider:  meters,
		SpanAttributes: []attribute.KeyValue{
			attribute.String("trace.only", "trace-value"),
		},
		MetricAttributes: []attribute.KeyValue{
			attribute.String("metric.only", "metric-value"),
		},
		StandardMetrics: true,
	})
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	ctx := WithOperation(context.Background(), "payments", "payments.lookup")
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://payments.internal:8443/path?token=secret", nil)
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}

	span := traces.spans()[0]
	if stringAttribute(span.attributes, "trace.only") != "trace-value" {
		t.Fatalf("span attributes = %s, want trace-only attribute", attributesString(span.attributes))
	}
	if _, found := findAttribute(span.attributes, attribute.Key("metric.only")); found {
		t.Fatalf("span attributes = %s, unexpectedly contain metric-only attribute", attributesString(span.attributes))
	}
	records := metrics.records()
	if len(records) != 1 || records[0].name != "http.client.request.duration" {
		t.Fatalf("metric records = %#v, want one standard duration record", records)
	}
	attributes := records[0].attributes.ToSlice()
	for key, want := range map[string]string{
		"http.request.method":             http.MethodGet,
		"server.address":                  "payments.internal",
		"error.type":                      "503",
		"network.protocol.version":        "2",
		"metric.only":                     "metric-value",
		clientkitotel.AttributeClientName: "payments",
		clientkitotel.AttributeProtocol:   "http",
		clientkitotel.AttributeOperation:  "payments.lookup",
	} {
		if got := stringAttribute(attributes, key); got != want {
			t.Fatalf("metric %s = %q, want %q (all attributes: %s)", key, got, want, attributesString(attributes))
		}
	}
	if got := intAttribute(attributes, "server.port"); got != 8443 {
		t.Fatalf("metric server.port = %d, want 8443", got)
	}
	if got := intAttribute(attributes, "http.response.status_code"); got != http.StatusServiceUnavailable {
		t.Fatalf("metric status code = %d, want 503", got)
	}
	serialized := attributesString(attributes)
	if strings.Contains(serialized, "trace.only") || strings.Contains(serialized, "token") || strings.Contains(serialized, "secret") || strings.Contains(serialized, "/path") {
		t.Fatalf("metric attributes leaked trace-only or request-target data: %s", serialized)
	}

	errorTransport, err := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("secret endpoint query token")
	}), Config{
		TracerProvider:  traces,
		MeterProvider:   meters,
		StandardMetrics: true,
	})
	if err != nil {
		t.Fatalf("error NewTransport() error = %v", err)
	}
	errorRequest, _ := http.NewRequest(http.MethodGet, "https://payments.internal/path?token=secret", nil)
	if _, err := errorTransport.RoundTrip(errorRequest); err == nil {
		t.Fatal("error RoundTrip() error = nil")
	}
	records = metrics.records()
	if len(records) != 2 {
		t.Fatalf("metric record count after error = %d, want 2", len(records))
	}
	errorAttributes := records[1].attributes.ToSlice()
	if got := stringAttribute(errorAttributes, "error.type"); got != "_OTHER" {
		t.Fatalf("arbitrary error type = %q, want _OTHER", got)
	}
	if serialized := attributesString(errorAttributes); strings.Contains(serialized, "secret") || strings.Contains(serialized, "token") || strings.Contains(serialized, "/path") {
		t.Fatalf("error metric attributes exposed raw error or request target: %s", serialized)
	}
}

func TestTransportDoesNotEmitStandardMetricsByDefault(t *testing.T) {
	meters, metrics := newTestMeterProvider()
	transport, err := NewTransport(roundTripperFunc(successResponse), Config{MeterProvider: meters})
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if records := metrics.records(); len(records) != 0 {
		t.Fatalf("default metric records = %#v, want none", records)
	}
}

func TestTransportClassifiesCancellationAndInvalidTransportResults(t *testing.T) {
	for _, test := range []struct {
		name string
		fn   roundTripperFunc
		want string
	}{
		{
			name: "canceled",
			fn: func(*http.Request) (*http.Response, error) {
				return nil, context.Canceled
			},
			want: "canceled",
		},
		{
			name: "nil response and nil error",
			fn: func(*http.Request) (*http.Response, error) {
				return nil, nil
			},
			want: "_OTHER",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			traces := newTestTracerProvider()
			transport, err := NewTransport(test.fn, Config{TracerProvider: traces})
			if err != nil {
				t.Fatalf("NewTransport() error = %v", err)
			}
			request, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
			response, gotErr := transport.RoundTrip(request)
			if response != nil || gotErr == nil {
				t.Fatalf("RoundTrip() = (%v, %v), want nil response and error", response, gotErr)
			}
			if test.want == "canceled" && !errors.Is(gotErr, context.Canceled) {
				t.Fatalf("RoundTrip() error = %v, want context.Canceled", gotErr)
			}
			span := traces.spans()[0]
			if span.status != codes.Error || stringAttribute(span.attributes, string(semconv.ErrorTypeKey)) != test.want {
				t.Fatalf("span = %#v, want error.type %q and error status", span, test.want)
			}
		})
	}
}

func TestTransportUsesConfiguredProviderForRemoteParent(t *testing.T) {
	traces := newTestTracerProvider()
	transport, err := NewTransport(roundTripperFunc(successResponse), Config{TracerProvider: traces})
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{2},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), remote)
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test", nil)
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	spans := traces.spans()
	if len(spans) != 1 || spans[0].parentSpanID != remote.SpanID() || spans[0].spanContext.TraceID() != remote.TraceID() {
		t.Fatalf("spans = %#v, want configured-provider child of remote parent", spans)
	}
}

func successResponse(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusNoContent,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    request,
	}, nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type idleClosingRoundTripper struct {
	closeCalls int
}

func (*idleClosingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.Canceled
}

func (transport *idleClosingRoundTripper) CloseIdleConnections() {
	transport.closeCalls++
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (body *trackedBody) Close() error {
	body.closed = true
	return nil
}

type testSpanSnapshot struct {
	name         string
	kind         trace.SpanKind
	spanContext  trace.SpanContext
	parentSpanID trace.SpanID
	attributes   []attribute.KeyValue
	status       codes.Code
	endCount     int
}

type testTracerProvider struct {
	trace.TracerProvider
	mu      sync.Mutex
	nextID  atomic.Uint64
	started []*testSpan
}

func newTestTracerProvider() *testTracerProvider {
	return &testTracerProvider{TracerProvider: tracenoop.NewTracerProvider()}
}

func (provider *testTracerProvider) Tracer(name string, options ...trace.TracerOption) trace.Tracer {
	return &testTracer{
		Tracer:   provider.TracerProvider.Tracer(name, options...),
		provider: provider,
	}
}

func (provider *testTracerProvider) spans() []testSpanSnapshot {
	provider.mu.Lock()
	spans := append([]*testSpan(nil), provider.started...)
	provider.mu.Unlock()
	result := make([]testSpanSnapshot, 0, len(spans))
	for _, span := range spans {
		result = append(result, span.snapshot())
	}
	return result
}

type testTracer struct {
	trace.Tracer
	provider *testTracerProvider
}

func (tracer *testTracer) Start(ctx context.Context, name string, options ...trace.SpanStartOption) (context.Context, trace.Span) {
	config := trace.NewSpanStartConfig(options...)
	parent := trace.SpanContextFromContext(ctx)
	id := tracer.provider.nextID.Add(1)
	var traceID trace.TraceID
	if parent.IsValid() {
		traceID = parent.TraceID()
	} else {
		traceID[0] = 1
		traceID[8] = byte(id >> 56)
		traceID[9] = byte(id >> 48)
		traceID[10] = byte(id >> 40)
		traceID[11] = byte(id >> 32)
		traceID[12] = byte(id >> 24)
		traceID[13] = byte(id >> 16)
		traceID[14] = byte(id >> 8)
		traceID[15] = byte(id)
	}
	var spanID trace.SpanID
	spanID[0] = 1
	spanID[1] = byte(id >> 48)
	spanID[2] = byte(id >> 40)
	spanID[3] = byte(id >> 32)
	spanID[4] = byte(id >> 24)
	spanID[5] = byte(id >> 16)
	spanID[6] = byte(id >> 8)
	spanID[7] = byte(id)
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	_, base := tracer.Tracer.Start(ctx, name, options...)
	span := &testSpan{
		Span:         base,
		provider:     tracer.provider,
		name:         name,
		kind:         config.SpanKind(),
		spanContext:  spanContext,
		parentSpanID: parent.SpanID(),
		attributes:   append([]attribute.KeyValue(nil), config.Attributes()...),
	}
	tracer.provider.mu.Lock()
	tracer.provider.started = append(tracer.provider.started, span)
	tracer.provider.mu.Unlock()
	return trace.ContextWithSpan(ctx, span), span
}

type testSpan struct {
	trace.Span
	provider     *testTracerProvider
	mu           sync.Mutex
	name         string
	kind         trace.SpanKind
	spanContext  trace.SpanContext
	parentSpanID trace.SpanID
	attributes   []attribute.KeyValue
	status       codes.Code
	endCount     int
}

func (span *testSpan) SpanContext() trace.SpanContext { return span.spanContext }
func (span *testSpan) IsRecording() bool              { return true }
func (span *testSpan) TracerProvider() trace.TracerProvider {
	return span.provider
}

func (span *testSpan) End(...trace.SpanEndOption) {
	span.mu.Lock()
	span.endCount++
	span.mu.Unlock()
}

func (span *testSpan) SetStatus(code codes.Code, _ string) {
	span.mu.Lock()
	span.status = code
	span.mu.Unlock()
}

func (span *testSpan) SetAttributes(attributes ...attribute.KeyValue) {
	span.mu.Lock()
	span.attributes = mergeAttributes(append(span.attributes, attributes...)...)
	span.mu.Unlock()
}

func (span *testSpan) snapshot() testSpanSnapshot {
	span.mu.Lock()
	defer span.mu.Unlock()
	return testSpanSnapshot{
		name:         span.name,
		kind:         span.kind,
		spanContext:  span.spanContext,
		parentSpanID: span.parentSpanID,
		attributes:   append([]attribute.KeyValue(nil), span.attributes...),
		status:       span.status,
		endCount:     span.endCount,
	}
}

type testMetricRecord struct {
	name       string
	attributes attribute.Set
}

type testMetricRecorder struct {
	mu     sync.Mutex
	values []testMetricRecord
}

func (recorder *testMetricRecorder) records() []testMetricRecord {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]testMetricRecord(nil), recorder.values...)
}

type testMeterProvider struct {
	metric.MeterProvider
	recorder *testMetricRecorder
}

func newTestMeterProvider() (*testMeterProvider, *testMetricRecorder) {
	recorder := &testMetricRecorder{}
	return &testMeterProvider{MeterProvider: metricnoop.NewMeterProvider(), recorder: recorder}, recorder
}

func (provider *testMeterProvider) Meter(name string, options ...metric.MeterOption) metric.Meter {
	return &testMeter{Meter: provider.MeterProvider.Meter(name, options...), recorder: provider.recorder}
}

type testMeter struct {
	metric.Meter
	recorder *testMetricRecorder
}

func (meter *testMeter) Float64Histogram(name string, options ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	instrument, err := meter.Meter.Float64Histogram(name, options...)
	return &testFloat64Histogram{Float64Histogram: instrument, name: name, recorder: meter.recorder}, err
}

type testFloat64Histogram struct {
	metric.Float64Histogram
	name     string
	recorder *testMetricRecorder
}

func (histogram *testFloat64Histogram) Record(_ context.Context, _ float64, options ...metric.RecordOption) {
	histogram.recorder.mu.Lock()
	histogram.recorder.values = append(histogram.recorder.values, testMetricRecord{
		name:       histogram.name,
		attributes: metric.NewRecordConfig(options).Attributes(),
	})
	histogram.recorder.mu.Unlock()
}

func (histogram *testFloat64Histogram) Enabled(context.Context) bool { return true }

func findAttribute(attributes []attribute.KeyValue, key attribute.Key) (attribute.KeyValue, bool) {
	for _, value := range attributes {
		if value.Key == key {
			return value, true
		}
	}
	return attribute.KeyValue{}, false
}

func stringAttribute(attributes []attribute.KeyValue, key string) string {
	value, _ := findAttribute(attributes, attribute.Key(key))
	return value.Value.AsString()
}

func intAttribute(attributes []attribute.KeyValue, key string) int64 {
	value, _ := findAttribute(attributes, attribute.Key(key))
	return value.Value.AsInt64()
}

func attributesString(attributes []attribute.KeyValue) string {
	var builder strings.Builder
	for _, value := range attributes {
		builder.WriteString(string(value.Key))
		builder.WriteByte('=')
		builder.WriteString(value.Value.Emit())
		builder.WriteByte(' ')
	}
	return builder.String()
}
