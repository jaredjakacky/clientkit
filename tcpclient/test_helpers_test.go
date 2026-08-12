package tcpclient_test

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/tcpclient"
)

func baseTCPConfig() tcpclient.Config {
	return tcpclient.Config{
		Config: clientkit.Config{
			Name:     "payments",
			Observer: clientkit.NopObserver{},
		},
		Address: "example.test:443",
	}
}

func newCustomTCPClient(t *testing.T, dial tcpclient.DialContextFunc, mutate func(*tcpclient.Config)) *tcpclient.Client {
	t.Helper()
	config := baseTCPConfig()
	config.DialContext = dial
	if mutate != nil {
		mutate(&config)
	}
	client, err := tcpclient.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

type trackedConnection struct {
	net.Conn
	closed atomic.Bool

	deadlineMu sync.Mutex
	deadline   time.Time
}

// deadlineIgnoringConnection lets cancellation-close tests isolate Close as
// the only mechanism capable of unblocking connection I/O.
type deadlineIgnoringConnection struct {
	*trackedConnection
}

type writeErrorConnection struct {
	*trackedConnection
	err error
}

type closeSignalingConnection struct {
	*trackedConnection
	closedSignal chan struct{}
	closeOnce    sync.Once
}

func (*deadlineIgnoringConnection) SetDeadline(time.Time) error { return nil }

func (c *writeErrorConnection) Write([]byte) (int, error) { return 0, c.err }

func (c *closeSignalingConnection) Close() error {
	c.closeOnce.Do(func() { close(c.closedSignal) })
	return c.trackedConnection.Close()
}

func (c *trackedConnection) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func (c *trackedConnection) SetDeadline(deadline time.Time) error {
	c.deadlineMu.Lock()
	c.deadline = deadline
	c.deadlineMu.Unlock()
	return c.Conn.SetDeadline(deadline)
}

func (c *trackedConnection) Deadline() time.Time {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	return c.deadline
}

func newTrackedPipe(t *testing.T) (*trackedConnection, net.Conn) {
	t.Helper()
	connection, peer := net.Pipe()
	tracked := &trackedConnection{Conn: connection}
	t.Cleanup(func() {
		_ = tracked.Close()
		_ = peer.Close()
	})
	return tracked, peer
}

func newCloseSignalingPipe(t *testing.T) (*closeSignalingConnection, net.Conn) {
	t.Helper()
	connection, peer := newTrackedPipe(t)
	return &closeSignalingConnection{
		trackedConnection: connection,
		closedSignal:      make(chan struct{}),
	}, peer
}

type tcpObserver struct {
	mu sync.Mutex

	starts   []clientkit.OperationStartEvent
	attempts []clientkit.AttemptEvent
	ends     []clientkit.OperationEndEvent
	health   []clientkit.HealthEvent
	order    []string
}

func (o *tcpObserver) StartOperation(ctx context.Context, event clientkit.OperationStartEvent) (context.Context, clientkit.OperationObservation) {
	o.mu.Lock()
	o.starts = append(o.starts, event)
	o.order = append(o.order, "start")
	o.mu.Unlock()
	return ctx, clientkit.OperationObservationFunc(func(_ context.Context, event clientkit.OperationEndEvent) {
		o.mu.Lock()
		o.ends = append(o.ends, event)
		o.order = append(o.order, "end")
		o.mu.Unlock()
	})
}

func (o *tcpObserver) ObserveAttempt(_ context.Context, event clientkit.AttemptEvent) {
	o.mu.Lock()
	o.attempts = append(o.attempts, event)
	o.order = append(o.order, "attempt")
	o.mu.Unlock()
}

func (*tcpObserver) ObserveRetry(context.Context, clientkit.RetryEvent) {}

func (o *tcpObserver) ObserveHealth(_ context.Context, event clientkit.HealthEvent) {
	o.mu.Lock()
	o.health = append(o.health, event)
	o.order = append(o.order, "health")
	o.mu.Unlock()
}

type tcpObserverEvents struct {
	starts   []clientkit.OperationStartEvent
	attempts []clientkit.AttemptEvent
	ends     []clientkit.OperationEndEvent
	health   []clientkit.HealthEvent
	order    []string
}

func (o *tcpObserver) snapshot() tcpObserverEvents {
	o.mu.Lock()
	defer o.mu.Unlock()
	return tcpObserverEvents{
		starts:   append([]clientkit.OperationStartEvent(nil), o.starts...),
		attempts: append([]clientkit.AttemptEvent(nil), o.attempts...),
		ends:     append([]clientkit.OperationEndEvent(nil), o.ends...),
		health:   append([]clientkit.HealthEvent(nil), o.health...),
		order:    append([]string(nil), o.order...),
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Conn = (*trackedConnection)(nil)
var _ net.Conn = (*deadlineIgnoringConnection)(nil)
var _ net.Conn = (*writeErrorConnection)(nil)
var _ net.Conn = (*closeSignalingConnection)(nil)
var _ clientkit.Observer = (*tcpObserver)(nil)
var _ net.Error = timeoutError{}
