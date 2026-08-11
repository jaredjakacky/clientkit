package httpotel

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	clientkitotel "github.com/jaredjakacky/clientkit/otel"
	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"
)

const InstrumentationScope = "github.com/jaredjakacky/clientkit/httpclient/otel"

// Config configures Clientkit's narrow per-RoundTrip OpenTelemetry adapter.
type Config struct {
	TracerProvider          trace.TracerProvider
	MeterProvider           metric.MeterProvider
	InstrumentationVersion  string
	Inject                  func(context.Context, http.Header)
	SpanAttributes          []attribute.KeyValue
	MetricAttributes        []attribute.KeyValue
	RequestTargetAttributes bool
	StandardMetrics         bool
}

// Transport records one CLIENT span for each RoundTrip invocation.
type Transport struct {
	base                    http.RoundTripper
	tracer                  trace.Tracer
	inject                  func(context.Context, http.Header)
	spanAttributes          []attribute.KeyValue
	metricAttributes        []attribute.KeyValue
	requestTargetAttributes bool
	duration                httpconv.ClientRequestDuration
	standardMetrics         bool
}

// NewTransport constructs a per-RoundTrip HTTP transport instrumenter.
func NewTransport(base http.RoundTripper, cfg Config) (*Transport, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	if cfg.TracerProvider == nil {
		cfg.TracerProvider = apiotel.GetTracerProvider()
	}

	tracerOptions := []trace.TracerOption(nil)
	meterOptions := []metric.MeterOption(nil)
	if cfg.InstrumentationVersion != "" {
		tracerOptions = append(tracerOptions, trace.WithInstrumentationVersion(cfg.InstrumentationVersion))
		meterOptions = append(meterOptions, metric.WithInstrumentationVersion(cfg.InstrumentationVersion))
	}

	transport := &Transport{
		base:                    base,
		tracer:                  cfg.TracerProvider.Tracer(InstrumentationScope, tracerOptions...),
		inject:                  cfg.Inject,
		spanAttributes:          cloneAttributes(cfg.SpanAttributes),
		metricAttributes:        cloneAttributes(cfg.MetricAttributes),
		requestTargetAttributes: cfg.RequestTargetAttributes,
		standardMetrics:         cfg.StandardMetrics,
	}
	if !cfg.StandardMetrics {
		return transport, nil
	}
	if cfg.MeterProvider == nil {
		cfg.MeterProvider = apiotel.GetMeterProvider()
	}
	duration, err := httpconv.NewClientRequestDuration(cfg.MeterProvider.Meter(InstrumentationScope, meterOptions...))
	if err != nil {
		return nil, err
	}
	transport.duration = duration
	return transport, nil
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t == nil || t.base == nil {
		return nil, errors.New("clientkit: HTTP telemetry transport is not configured")
	}
	if request == nil {
		return nil, errors.New("clientkit: HTTP telemetry transport requires a request")
	}

	startedAt := time.Now()
	metadata := metadataFromContext(request.Context())
	resendCount := metadata.nextResendCount()
	method, known := knownMethod(request.Method)

	attributes := make([]attribute.KeyValue, 0, len(t.spanAttributes)+12)
	attributes = append(attributes, t.spanAttributes...)
	attributes = append(attributes,
		semconv.HTTPRequestMethodKey.String(string(method)),
		attribute.String(clientkitotel.AttributeProtocol, "http"),
	)
	if !known && request.Method != "" {
		attributes = append(attributes, semconv.HTTPRequestMethodOriginal(request.Method))
	}
	if metadata.state != nil {
		attributes = append(attributes,
			attribute.String(clientkitotel.AttributeClientName, metadata.state.client),
			attribute.String(clientkitotel.AttributeOperation, metadata.state.operation),
		)
	}
	if metadata.attempt > 0 {
		attributes = append(attributes, attribute.Int(clientkitotel.AttributeAttemptNumber, metadata.attempt))
	}
	if resendCount > 0 {
		attributes = append(attributes, semconv.HTTPRequestResendCount(resendCount))
	}
	if t.requestTargetAttributes {
		attributes = append(attributes, requestTargetSpanAttributes(request)...)
	}

	ctx, span := t.tracer.Start(
		request.Context(),
		spanName(method, known),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(startedAt),
		trace.WithAttributes(mergeAttributes(attributes...)...),
	)
	attemptRequest := request.Clone(ctx)
	attemptRequest.Header = request.Header.Clone()
	if attemptRequest.Header == nil {
		attemptRequest.Header = make(http.Header)
	}
	injectSafely(t.inject, ctx, attemptRequest.Header)

	response, err := t.base.RoundTrip(attemptRequest)
	if response == nil && err == nil {
		err = errors.New("clientkit: HTTP transport returned no response and no error")
	}
	endedAt := time.Now()
	endAttributes, errorType, failed := responseAttributes(response, err)
	span.SetAttributes(endAttributes...)
	if failed {
		span.SetStatus(codes.Error, "")
	}
	if t.standardMetrics {
		t.recordDuration(ctx, attemptRequest, response, method, errorType, endedAt.Sub(startedAt), metadata)
	}
	span.End(trace.WithTimestamp(endedAt))
	return response, err
}

func (t *Transport) recordDuration(ctx context.Context, request *http.Request, response *http.Response, method httpconv.RequestMethodAttr, errorType string, duration time.Duration, metadata operationMetadata) {
	address, port := serverAddressPort(request)
	attributes := cloneAttributes(t.metricAttributes)
	if metadata.state != nil {
		attributes = append(attributes,
			attribute.String(clientkitotel.AttributeClientName, metadata.state.client),
			attribute.String(clientkitotel.AttributeProtocol, "http"),
			attribute.String(clientkitotel.AttributeOperation, metadata.state.operation),
		)
	}
	if response != nil {
		attributes = append(attributes, semconv.HTTPResponseStatusCode(response.StatusCode))
		if version := protocolVersion(response); version != "" {
			attributes = append(attributes, semconv.NetworkProtocolVersion(version))
		}
	}
	if errorType != "" {
		attributes = append(attributes, semconv.ErrorTypeKey.String(errorType))
	}
	t.duration.Record(ctx, duration.Seconds(), method, address, port, mergeAttributes(attributes...)...)
}

func responseAttributes(response *http.Response, err error) ([]attribute.KeyValue, string, bool) {
	attributes := make([]attribute.KeyValue, 0, 3)
	if response != nil {
		attributes = append(attributes, semconv.HTTPResponseStatusCode(response.StatusCode))
		if version := protocolVersion(response); version != "" {
			attributes = append(attributes, semconv.NetworkProtocolVersion(version))
		}
	}
	errorType := httpErrorType(response, err)
	if errorType != "" {
		attributes = append(attributes, semconv.ErrorTypeKey.String(errorType))
	}
	failed := errorType != ""
	return attributes, errorType, failed
}

func httpErrorType(response *http.Response, err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if err != nil {
		var netError net.Error
		if errors.As(err, &netError) && netError.Timeout() {
			return "timeout"
		}
		var dnsError *net.DNSError
		if errors.As(err, &dnsError) {
			return "dns_error"
		}
		var tlsError *tls.CertificateVerificationError
		if errors.As(err, &tlsError) {
			return "tls_error"
		}
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return "connection_closed"
		}
		return "_OTHER"
	}
	if response != nil && response.StatusCode >= http.StatusBadRequest {
		return strconv.Itoa(response.StatusCode)
	}
	return ""
}

func knownMethod(method string) (httpconv.RequestMethodAttr, bool) {
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case http.MethodConnect:
		return httpconv.RequestMethodConnect, true
	case http.MethodDelete:
		return httpconv.RequestMethodDelete, true
	case http.MethodGet:
		return httpconv.RequestMethodGet, true
	case http.MethodHead:
		return httpconv.RequestMethodHead, true
	case http.MethodOptions:
		return httpconv.RequestMethodOptions, true
	case http.MethodPatch:
		return httpconv.RequestMethodPatch, true
	case http.MethodPost:
		return httpconv.RequestMethodPost, true
	case http.MethodPut:
		return httpconv.RequestMethodPut, true
	case http.MethodTrace:
		return httpconv.RequestMethodTrace, true
	case "QUERY":
		return httpconv.RequestMethodQuery, true
	default:
		return httpconv.RequestMethodOther, false
	}
}

func spanName(method httpconv.RequestMethodAttr, known bool) string {
	if !known {
		return "HTTP"
	}
	return string(method)
}

func protocolVersion(response *http.Response) string {
	if response == nil {
		return ""
	}
	switch {
	case response.ProtoMajor == 1 && response.ProtoMinor == 0:
		return "1.0"
	case response.ProtoMajor == 1 && response.ProtoMinor == 1:
		return "1.1"
	case response.ProtoMajor == 2:
		return "2"
	case response.ProtoMajor == 3:
		return "3"
	default:
		return ""
	}
}

func requestTargetSpanAttributes(request *http.Request) []attribute.KeyValue {
	address, port := serverAddressPort(request)
	attributes := make([]attribute.KeyValue, 0, 3)
	if address != "" {
		attributes = append(attributes, semconv.ServerAddress(address))
	}
	if port > 0 {
		attributes = append(attributes, semconv.ServerPort(port))
	}
	if full := sanitizedURL(request); full != "" {
		attributes = append(attributes, semconv.URLFull(full))
	}
	return attributes
}

func serverAddressPort(request *http.Request) (string, int) {
	if request == nil || request.URL == nil {
		return "", 0
	}
	authority := request.URL.Host
	if request.Host != "" {
		authority = request.Host
	}
	parsed := url.URL{Host: authority}
	address := parsed.Hostname()
	port, _ := strconv.Atoi(parsed.Port())
	if port == 0 {
		switch strings.ToLower(request.URL.Scheme) {
		case "http":
			port = 80
		case "https":
			port = 443
		}
	}
	return address, port
}

func sanitizedURL(request *http.Request) string {
	if request == nil || request.URL == nil {
		return ""
	}
	value := *request.URL
	value.User = nil
	value.Fragment = ""
	value.RawFragment = ""
	if value.RawQuery != "" || value.ForceQuery {
		query := value.Query()
		for key, values := range query {
			for index := range values {
				values[index] = "REDACTED"
			}
			query[key] = values
		}
		value.RawQuery = query.Encode()
	}
	return value.String()
}

func injectSafely(inject func(context.Context, http.Header), ctx context.Context, headers http.Header) {
	if inject == nil || headers == nil {
		return
	}
	backup := headers.Clone()
	completed := false
	defer func() {
		if recover() != nil || !completed {
			clear(headers)
			for key, values := range backup {
				headers[key] = append([]string(nil), values...)
			}
		}
	}()
	inject(ctx, headers)
	completed = true
}

func cloneAttributes(attributes []attribute.KeyValue) []attribute.KeyValue {
	return append([]attribute.KeyValue(nil), attributes...)
}

func mergeAttributes(attributes ...attribute.KeyValue) []attribute.KeyValue {
	merged := make([]attribute.KeyValue, 0, len(attributes))
	positions := make(map[attribute.Key]int, len(attributes))
	for _, value := range attributes {
		if strings.TrimSpace(string(value.Key)) == "" {
			continue
		}
		if position, exists := positions[value.Key]; exists {
			merged[position] = value
			continue
		}
		positions[value.Key] = len(merged)
		merged = append(merged, value)
	}
	return merged
}
