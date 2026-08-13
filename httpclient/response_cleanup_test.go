package httpclient_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

const discardedResponseBodyLimit = 64 << 10

func TestHTTPRetryReusesConnectionAfterSmallDiscardedBody(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		name := "plain"
		if compressed {
			name = "gzip"
		}
		t.Run(name, func(t *testing.T) {
			payload := []byte(`{"status":"temporarily unavailable"}`)
			wireBody := payload
			if compressed {
				var encoded bytes.Buffer
				writer := gzip.NewWriter(&encoded)
				if _, err := writer.Write(payload); err != nil {
					t.Fatalf("compress retry body: %v", err)
				}
				if err := writer.Close(); err != nil {
					t.Fatalf("close gzip writer: %v", err)
				}
				wireBody = encoded.Bytes()
			}

			var requests atomic.Int64
			server, connections := newTrackedHTTP1Server(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				if requests.Add(1) == 1 {
					if compressed {
						response.Header().Set("Content-Encoding", "gzip")
					}
					response.Header().Set("Content-Length", strconv.Itoa(len(wireBody)))
					response.WriteHeader(http.StatusServiceUnavailable)
					_, _ = response.Write(wireBody)
					return
				}
				response.WriteHeader(http.StatusNoContent)
			}))

			client := newServerHTTPClient(t, server.URL, httpclient.Config{Retry: responseRetryPolicy()})
			request, err := http.NewRequest(http.MethodGet, server.URL+"/resource", nil)
			if err != nil {
				t.Fatalf("construct request: %v", err)
			}
			result := client.Execute(request)
			if result.Response != nil {
				defer result.Response.Body.Close()
			}
			if result.Err != nil || result.Outcome != httpclient.OutcomeSuccess || len(result.Attempts) != 2 {
				t.Fatalf("Execute() = %#v, want successful retry", result)
			}
			if got := requests.Load(); got != 2 {
				t.Fatalf("requests = %d, want 2", got)
			}
			if got := connections.Load(); got != 1 {
				t.Fatalf("HTTP/1.1 connections = %d, want 1", got)
			}
		})
	}
}

func TestHTTPCheckReusesConnectionAfterSmallDiscardedBody(t *testing.T) {
	var requests atomic.Int64
	server, connections := newTrackedHTTP1Server(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		response.Header().Set("Content-Length", "2")
		response.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(response, "ok")
	}))

	client := newServerHTTPClient(t, server.URL, httpclient.Config{Check: httpclient.CheckConfig{
		Enabled: true,
		Path:    "/healthz",
	}})
	for check := 1; check <= 2; check++ {
		health := client.Check(context.Background())
		if health.State != clientkit.HealthHealthy {
			t.Fatalf("Check() %d = %#v, want healthy", check, health)
		}
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("HTTP/1.1 connections = %d, want 1", got)
	}
}

func TestRetryBoundsDiscardedResponseDrain(t *testing.T) {
	for _, test := range []struct {
		name          string
		contentLength int64
		bodySize      int64
		wantRead      int64
	}{
		{name: "unknown exact limit", contentLength: -1, bodySize: discardedResponseBodyLimit, wantRead: discardedResponseBodyLimit},
		{name: "unknown over limit", contentLength: -1, bodySize: discardedResponseBodyLimit + 1024, wantRead: discardedResponseBodyLimit + 1},
		{name: "known exact limit", contentLength: discardedResponseBodyLimit, bodySize: discardedResponseBodyLimit, wantRead: discardedResponseBodyLimit},
		{name: "known over limit", contentLength: discardedResponseBodyLimit + 1, bodySize: discardedResponseBodyLimit + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &trackingCleanupBody{Reader: io.LimitReader(zeroReader{}, test.bodySize)}
			calls := 0
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return discardedRetryResponse(request, http.StatusServiceUnavailable, 1, test.contentLength, body), nil
				}
				return discardedRetryResponse(request, http.StatusNoContent, 1, 0, http.NoBody), nil
			}, httpclient.Config{Retry: responseRetryPolicy()})

			result := client.Execute(mustRequest(t, "https://example.test/resource"))
			if result.Err != nil || result.Outcome != httpclient.OutcomeSuccess || len(result.Attempts) != 2 {
				t.Fatalf("Execute() = %#v, want successful retry", result)
			}
			if body.bytesRead != test.wantRead || !body.closed {
				t.Fatalf("discarded body bytes/closed = %d/%t, want %d/true", body.bytesRead, body.closed, test.wantRead)
			}
		})
	}
}

func TestRetryClosesDiscardedResponseAfterReadError(t *testing.T) {
	readErr := errors.New("response read failed")
	body := &trackingCleanupBody{Reader: errorReader{err: readErr}}
	calls := 0
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return discardedRetryResponse(request, http.StatusServiceUnavailable, 1, -1, body), nil
		}
		return discardedRetryResponse(request, http.StatusNoContent, 1, 0, http.NoBody), nil
	}, httpclient.Config{Retry: responseRetryPolicy()})

	result := client.Execute(mustRequest(t, "https://example.test/resource"))
	if result.Err != nil || result.Outcome != httpclient.OutcomeSuccess || len(result.Attempts) != 2 {
		t.Fatalf("Execute() = %#v, want cleanup error not to replace successful retry", result)
	}
	if body.readCalls != 1 || !body.closed {
		t.Fatalf("discarded body reads/closed = %d/%t, want 1/true", body.readCalls, body.closed)
	}
}

func TestRetryDiscardedResponseDrainHonorsAttemptTimeout(t *testing.T) {
	readStarted := make(chan struct{})
	var body *blockingCleanupBody
	calls := 0
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			body = &blockingCleanupBody{ctx: request.Context(), readStarted: readStarted}
			return discardedRetryResponse(request, http.StatusServiceUnavailable, 1, -1, body), nil
		}
		return discardedRetryResponse(request, http.StatusNoContent, 1, 0, http.NoBody), nil
	}, httpclient.Config{
		AttemptTimeout: 25 * time.Millisecond,
		Timeout:        time.Second,
		Retry:          responseRetryPolicy(),
	})

	startedAt := time.Now()
	result := client.Execute(mustRequest(t, "https://example.test/resource"))
	if result.Err != nil || result.Outcome != httpclient.OutcomeSuccess || len(result.Attempts) != 2 {
		t.Fatalf("Execute() = %#v, want retry after bounded cleanup", result)
	}
	select {
	case <-readStarted:
	default:
		t.Fatal("discarded streaming body was not read")
	}
	if body == nil || !body.closed.Load() {
		t.Fatal("discarded streaming body was not closed")
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("Execute() took %v, want attempt timeout to remain authoritative", elapsed)
	}
}

func TestRetryDiscardedResponseDrainHonorsTotalTimeout(t *testing.T) {
	readStarted := make(chan struct{})
	var body *blockingCleanupBody
	calls := 0
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		body = &blockingCleanupBody{ctx: request.Context(), readStarted: readStarted}
		return discardedRetryResponse(request, http.StatusServiceUnavailable, 1, -1, body), nil
	}, httpclient.Config{
		DisableAttemptTimeout: true,
		Timeout:               25 * time.Millisecond,
		Retry:                 responseRetryPolicy(),
	})

	startedAt := time.Now()
	result := client.Execute(mustRequest(t, "https://example.test/resource"))
	if result.Outcome != httpclient.OutcomeTimeout || result.FailureClass != clientkit.FailureTimeout ||
		!errors.Is(result.Err, context.DeadlineExceeded) || len(result.Attempts) != 1 || calls != 1 {
		t.Fatalf("Execute() = %#v with %d calls, want total timeout after one response", result, calls)
	}
	select {
	case <-readStarted:
	default:
		t.Fatal("discarded streaming body was not read")
	}
	if body == nil || !body.closed.Load() {
		t.Fatal("discarded streaming body was not closed")
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("Execute() took %v, want total timeout to remain authoritative", elapsed)
	}
}

func TestHealthDiscardedResponseDrainHonorsCheckTimeout(t *testing.T) {
	readStarted := make(chan struct{})
	var body *blockingCleanupBody
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		body = &blockingCleanupBody{ctx: request.Context(), readStarted: readStarted}
		return discardedRetryResponse(request, http.StatusOK, 1, -1, body), nil
	}, httpclient.Config{Check: httpclient.CheckConfig{
		Enabled: true,
		Path:    "/healthz",
		Timeout: 25 * time.Millisecond,
	}})

	startedAt := time.Now()
	health := client.Check(context.Background())
	if health.State != clientkit.HealthHealthy || health.FailureClass != clientkit.FailureNone {
		t.Fatalf("Check() = %#v, want classified health unchanged by cleanup", health)
	}
	select {
	case <-readStarted:
	default:
		t.Fatal("discarded health body was not read")
	}
	if body == nil || !body.closed.Load() {
		t.Fatal("discarded health body was not closed")
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("Check() took %v, want check timeout to remain authoritative", elapsed)
	}
}

func TestFinalCallerOwnedResponseIsNotDrainedOrClosed(t *testing.T) {
	body := &trackingCleanupBody{Reader: strings.NewReader("caller payload")}
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ProtoMajor:    1,
			Header:        make(http.Header),
			Body:          body,
			ContentLength: int64(len("caller payload")),
			Request:       request,
		}, nil
	}, httpclient.Config{Retry: httpclient.NoRetryConfig()})

	result := client.Execute(mustRequest(t, "https://example.test/resource"))
	if result.Err != nil || result.Response == nil || result.Outcome != httpclient.OutcomeSuccess {
		t.Fatalf("Execute() = %#v, want caller-owned response", result)
	}
	if body.bytesRead != 0 || body.closed {
		t.Fatalf("caller body bytes/closed = %d/%t, want 0/false", body.bytesRead, body.closed)
	}
	payload, err := io.ReadAll(result.Response.Body)
	if err != nil {
		t.Fatalf("read caller response: %v", err)
	}
	if err := result.Response.Body.Close(); err != nil {
		t.Fatalf("close caller response: %v", err)
	}
	if string(payload) != "caller payload" || !body.closed {
		t.Fatalf("caller payload/closed = %q/%t", payload, body.closed)
	}
}

func TestRetryDoesNotDrainResponsesWithoutHTTP1ReuseBenefit(t *testing.T) {
	for _, test := range []struct {
		name            string
		method          string
		statusCode      int
		protoMajor      int
		responseClose   bool
		disableTimeouts bool
		upgraded        bool
	}{
		{name: "HTTP/2", method: http.MethodGet, statusCode: http.StatusServiceUnavailable, protoMajor: 2},
		{name: "connection close", method: http.MethodGet, statusCode: http.StatusServiceUnavailable, protoMajor: 1, responseClose: true},
		{name: "HEAD", method: http.MethodHead, statusCode: http.StatusServiceUnavailable, protoMajor: 1},
		{name: "no deadline", method: http.MethodGet, statusCode: http.StatusServiceUnavailable, protoMajor: 1, disableTimeouts: true},
		{name: "protocol upgrade", method: http.MethodGet, statusCode: http.StatusSwitchingProtocols, protoMajor: 1, upgraded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &trackingCleanupBody{Reader: strings.NewReader("discarded")}
			var upgradeBody *upgradedResponseBody
			calls := 0
			statusCodes := []int{test.statusCode}
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					responseBody := io.ReadCloser(body)
					if test.upgraded {
						upgradeBody = &upgradedResponseBody{Reader: strings.NewReader("upgraded")}
						responseBody = upgradeBody
					}
					response := discardedRetryResponse(request, test.statusCode, test.protoMajor, -1, responseBody)
					response.Close = test.responseClose
					return response, nil
				}
				return discardedRetryResponse(request, http.StatusNoContent, 1, 0, http.NoBody), nil
			}, httpclient.Config{
				DisableTimeout:        test.disableTimeouts,
				DisableAttemptTimeout: test.disableTimeouts,
				Retry: httpclient.RetryConfig{
					MaxAttempts: 2,
					StatusCodes: statusCodes,
					Methods:     []string{test.method},
				},
			})

			request, err := http.NewRequest(test.method, "https://example.test/resource", nil)
			if err != nil {
				t.Fatalf("construct request: %v", err)
			}
			result := client.Execute(request)
			if result.Err != nil || result.Outcome != httpclient.OutcomeSuccess || len(result.Attempts) != 2 {
				t.Fatalf("Execute() = %#v, want successful retry", result)
			}
			if test.upgraded {
				if upgradeBody == nil || !upgradeBody.closed {
					t.Fatal("discarded upgraded body was not closed")
				}
				remaining, err := io.ReadAll(upgradeBody.Reader)
				if err != nil || string(remaining) != "upgraded" {
					t.Fatalf("upgraded body remaining data/error = %q/%v, want untouched data", remaining, err)
				}
				return
			}
			if body.bytesRead != 0 || !body.closed {
				t.Fatalf("discarded body bytes/closed = %d/%t, want 0/true", body.bytesRead, body.closed)
			}
		})
	}
}

func responseRetryPolicy() httpclient.RetryConfig {
	return httpclient.RetryConfig{
		MaxAttempts: 2,
		StatusCodes: []int{http.StatusServiceUnavailable},
		Methods:     []string{http.MethodGet},
	}
}

func discardedRetryResponse(request *http.Request, statusCode, protoMajor int, contentLength int64, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode:    statusCode,
		ProtoMajor:    protoMajor,
		Header:        make(http.Header),
		Body:          body,
		ContentLength: contentLength,
		Request:       request,
	}
}

func newServerHTTPClient(t *testing.T, baseURL string, cfg httpclient.Config) *httpclient.Client {
	t.Helper()
	transport := httpclient.DefaultTransport()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	cfg.Config = clientkit.Config{Name: "response-cleanup", Observer: clientkit.NopObserver{}}
	cfg.BaseURL = baseURL
	cfg.HTTPClient = &http.Client{Transport: transport}
	cfg.Propagator = httpclient.NopHeaderPropagator{}
	client, err := httpclient.New(cfg)
	if err != nil {
		t.Fatalf("httpclient.New() error = %v", err)
	}
	return client
}

func newTrackedHTTP1Server(t *testing.T, handler http.Handler) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	connections := new(atomic.Int64)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server, connections
}

type trackingCleanupBody struct {
	io.Reader
	readCalls int
	bytesRead int64
	closed    bool
}

func (body *trackingCleanupBody) Read(buffer []byte) (int, error) {
	body.readCalls++
	read, err := body.Reader.Read(buffer)
	body.bytesRead += int64(read)
	return read, err
}

func (body *trackingCleanupBody) Close() error {
	body.closed = true
	return nil
}

type blockingCleanupBody struct {
	ctx         context.Context
	readStarted chan struct{}
	startOnce   sync.Once
	closed      atomic.Bool
}

func (body *blockingCleanupBody) Read([]byte) (int, error) {
	body.startOnce.Do(func() { close(body.readStarted) })
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (body *blockingCleanupBody) Close() error {
	body.closed.Store(true)
	return nil
}

type errorReader struct {
	err error
}

func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}
