package tcpclient_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/tcpclient"
)

func TestNewValidatesTLSConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tcpclient.Config)
	}{
		{name: "disabled with config", mutate: func(config *tcpclient.Config) {
			config.TLS.Config = &tls.Config{}
		}},
		{name: "disabled with server name", mutate: func(config *tcpclient.Config) {
			config.TLS.ServerName = "example.test"
		}},
		{name: "disabled with inference control", mutate: func(config *tcpclient.Config) {
			config.TLS.DisableServerNameInference = true
		}},
		{name: "disabled with handshake timeout", mutate: func(config *tcpclient.Config) {
			config.TLS.HandshakeTimeout = time.Second
		}},
		{name: "blank explicit server name", mutate: func(config *tcpclient.Config) {
			config.TLS = tcpclient.TLSConfig{Enabled: true, ServerName: " "}
		}},
		{name: "conflicting server names", mutate: func(config *tcpclient.Config) {
			config.TLS = tcpclient.TLSConfig{
				Enabled: true, ServerName: "example.test",
				Config: &tls.Config{ServerName: "other.test"},
			}
		}},
		{name: "verification without server name", mutate: func(config *tcpclient.Config) {
			config.Address = "dialer-address"
			config.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
			config.TLS = tcpclient.TLSConfig{Enabled: true, DisableServerNameInference: true}
		}},
		{name: "server name cannot be inferred from custom address", mutate: func(config *tcpclient.Config) {
			config.Address = "dialer-address"
			config.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
			config.TLS.Enabled = true
		}},
		{name: "negative handshake timeout", mutate: func(config *tcpclient.Config) {
			config.TLS = tcpclient.TLSConfig{Enabled: true, HandshakeTimeout: -time.Second}
		}},
		{name: "disabled configured handshake timeout", mutate: func(config *tcpclient.Config) {
			config.TLS = tcpclient.TLSConfig{Enabled: true, HandshakeTimeout: time.Second, DisableHandshakeTimeout: true}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := baseTCPConfig()
			test.mutate(&config)
			client, err := tcpclient.New(config)
			if err == nil || client != nil {
				t.Fatalf("New() = (%v, %v), want TLS validation error", client, err)
			}
		})
	}
}

func TestNewAcceptsTLSServerNamePolicies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tcpclient.Config)
	}{
		{name: "address inference", mutate: func(config *tcpclient.Config) {
			config.TLS.Enabled = true
		}},
		{name: "explicit server name", mutate: func(config *tcpclient.Config) {
			config.TLS = tcpclient.TLSConfig{Enabled: true, ServerName: " example.test "}
		}},
		{name: "explicit server name with inference disabled", mutate: func(config *tcpclient.Config) {
			config.TLS = tcpclient.TLSConfig{Enabled: true, ServerName: "example.test", DisableServerNameInference: true}
		}},
		{name: "tls config server name", mutate: func(config *tcpclient.Config) {
			config.TLS = tcpclient.TLSConfig{Enabled: true, Config: &tls.Config{ServerName: "example.test"}}
		}},
		{name: "verification deliberately disabled", mutate: func(config *tcpclient.Config) {
			config.Address = "dialer-address"
			config.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
			config.TLS = tcpclient.TLSConfig{
				Enabled: true, DisableServerNameInference: true,
				Config: &tls.Config{InsecureSkipVerify: true},
			}
		}},
		{name: "IPv6 address inference", mutate: func(config *tcpclient.Config) {
			config.Network = "tcp6"
			config.Address = "[::1]:443"
			config.TLS.Enabled = true
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := baseTCPConfig()
			test.mutate(&config)
			client, err := tcpclient.New(config)
			if err != nil || client == nil {
				t.Fatalf("New() = (%v, %v), want accepted TLS policy", client, err)
			}
		})
	}
}

func TestTLSDialHandshakeAndPolicyCloning(t *testing.T) {
	versions := []struct {
		name    string
		version uint16
		want    string
	}{
		{name: "legacy version is not a telemetry dimension", version: tls.VersionTLS11, want: ""},
		{name: "TLS 1.2", version: tls.VersionTLS12, want: "1.2"},
		{name: "TLS 1.3", version: tls.VersionTLS13, want: "1.3"},
	}

	for _, test := range versions {
		t.Run(test.name, func(t *testing.T) {
			certificate, roots := testCertificate(t, "example.test")
			serverConfig := &tls.Config{
				Certificates: []tls.Certificate{certificate},
				MinVersion:   test.version,
				MaxVersion:   test.version,
			}
			clientPolicy := &tls.Config{
				RootCAs:    roots,
				MinVersion: test.version,
				MaxVersion: test.version,
			}
			observer := &tcpObserver{}
			serverResults := make(chan error, 1)
			client := newCustomTCPClient(t, tlsPipeDialer(t, serverConfig, serverResults), func(config *tcpclient.Config) {
				config.Observer = observer
				config.TLS = tcpclient.TLSConfig{Enabled: true, Config: clientPolicy}
			})

			// Mutating the caller's policy after construction must not alter the
			// immutable policy used by the client.
			clientPolicy.RootCAs = nil
			clientPolicy.ServerName = "wrong.test"
			clientPolicy.MinVersion = tls.VersionTLS13

			result := client.DialResult(context.Background())
			if result.Err != nil || result.Outcome != tcpclient.OutcomeSuccess || result.FailureClass != clientkit.FailureNone {
				t.Fatalf("DialResult() = %#v, want successful TLS handshake", result)
			}
			secure, ok := result.Conn.(*tls.Conn)
			if !ok {
				t.Fatalf("connection = %T, want *tls.Conn", result.Conn)
			}
			if got := secure.ConnectionState().Version; got != test.version {
				t.Fatalf("TLS version = %d, want %d", got, test.version)
			}
			if err := <-serverResults; err != nil {
				t.Fatalf("server handshake error = %v", err)
			}
			_ = result.Conn.Close()

			events := observer.snapshot()
			if len(events.attempts) != 1 || len(events.ends) != 1 {
				t.Fatalf("observer events = (%d attempts, %d ends), want one each", len(events.attempts), len(events.ends))
			}
			if got := attributeValue(events.attempts[0].Attributes, "tls.version"); got != test.want {
				t.Fatalf("tls.version = %q, want %q", got, test.want)
			}
			if got := attributeValue(events.ends[0].Attributes, "client.security"); got != "tls" {
				t.Fatalf("client.security = %q, want tls", got)
			}
		})
	}
}

func TestTLSDialFailureClassification(t *testing.T) {
	t.Run("invalid TLS record", func(t *testing.T) {
		connection, peer := newTrackedPipe(t)
		observer := &tcpObserver{}
		go func() {
			// Read the ClientHello before replying: net.Pipe is unbuffered, so
			// writing immediately would deadlock with the client's first write.
			buffer := make([]byte, 4096)
			_, _ = peer.Read(buffer)
			_, _ = peer.Write([]byte("abcde"))
			_ = peer.Close()
		}()
		client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
			return connection, nil
		}, func(config *tcpclient.Config) {
			config.Observer = observer
			config.TLS = tcpclient.TLSConfig{Enabled: true, Config: &tls.Config{InsecureSkipVerify: true}}
		})
		result := client.DialResult(context.Background())
		if result.Outcome != tcpclient.OutcomeTLSError || result.FailureClass != clientkit.FailureTLS || result.Err == nil || !connection.closed.Load() {
			t.Fatalf("DialResult() = %#v, closed=%t; want closed TLS failure", result, connection.closed.Load())
		}
		var recordError tls.RecordHeaderError
		if !errors.As(result.Err, &recordError) {
			t.Fatalf("Result.Err = %T %v, want original tls.RecordHeaderError", result.Err, result.Err)
		}
		events := observer.snapshot()
		if len(events.attempts) != 1 || len(events.ends) != 1 {
			t.Fatalf("observer events = (%d attempts, %d ends), want one each", len(events.attempts), len(events.ends))
		}
		if events.attempts[0].Err == nil || events.ends[0].Err == nil || events.attempts[0].Err.Error() != "clientkit: TLS handshake failed" || events.ends[0].Err.Error() != "clientkit: TLS handshake failed" {
			t.Fatalf("TLS observer errors = (%v, %v), want stable handshake error", events.attempts[0].Err, events.ends[0].Err)
		}
	})

	t.Run("EOF during handshake remains TLS failure", func(t *testing.T) {
		connection, peer := newTrackedPipe(t)
		_ = peer.Close()
		client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
			return connection, nil
		}, func(config *tcpclient.Config) {
			config.TLS = tcpclient.TLSConfig{Enabled: true, Config: &tls.Config{InsecureSkipVerify: true}}
		})
		result := client.DialResult(context.Background())
		if result.Outcome != tcpclient.OutcomeTLSError || result.FailureClass != clientkit.FailureTLS {
			t.Fatalf("classification = (%q, %q), want TLS failure", result.Outcome, result.FailureClass)
		}
	})

	t.Run("handshake cancellation", func(t *testing.T) {
		connection, peer := newTrackedPipe(t)
		handshakeStarted := make(chan struct{})
		go func() {
			buffer := make([]byte, 1)
			_, _ = peer.Read(buffer)
			close(handshakeStarted)
		}()
		observer := &tcpObserver{}
		client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
			return connection, nil
		}, func(config *tcpclient.Config) {
			config.Observer = observer
			config.TLS = tcpclient.TLSConfig{Enabled: true, Config: &tls.Config{InsecureSkipVerify: true}}
		})
		ctx, cancel := context.WithCancel(context.Background())
		results := make(chan tcpclient.Result, 1)
		go func() {
			results <- client.DialResult(ctx)
		}()
		select {
		case <-handshakeStarted:
		case <-time.After(time.Second):
			t.Fatal("TLS handshake did not start")
		}
		cancel()
		var result tcpclient.Result
		select {
		case result = <-results:
		case <-time.After(time.Second):
			t.Fatal("DialResult did not return after handshake cancellation")
		}
		if result.Outcome != tcpclient.OutcomeCanceled || result.FailureClass != clientkit.FailureCanceled {
			t.Fatalf("classification = (%q, %q), want canceled", result.Outcome, result.FailureClass)
		}
		if !errors.Is(result.Err, context.Canceled) || !connection.closed.Load() {
			t.Fatalf("DialResult() = %#v, closed=%t; want canceled closed handshake", result, connection.closed.Load())
		}
		events := observer.snapshot()
		if len(events.attempts) != 1 {
			t.Fatalf("attempt events = %d, want 1", len(events.attempts))
		}
		if !errors.Is(events.attempts[0].Err, context.Canceled) {
			t.Fatalf("attempt error = %v, want context.Canceled", events.attempts[0].Err)
		}
	})

	t.Run("handshake timeout", func(t *testing.T) {
		connection, _ := newTrackedPipe(t)
		observer := &tcpObserver{}
		client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
			return connection, nil
		}, func(config *tcpclient.Config) {
			config.Observer = observer
			config.TLS = tcpclient.TLSConfig{
				Enabled: true, Config: &tls.Config{InsecureSkipVerify: true},
				HandshakeTimeout: 10 * time.Millisecond,
			}
		})
		result := client.DialResult(context.Background())
		if result.Outcome != tcpclient.OutcomeTimeout || result.FailureClass != clientkit.FailureTimeout {
			t.Fatalf("classification = (%q, %q), want timeout", result.Outcome, result.FailureClass)
		}
		events := observer.snapshot()
		if len(events.attempts) != 1 {
			t.Fatalf("attempt events = %d, want 1", len(events.attempts))
		}
		if !errors.Is(events.attempts[0].Err, context.DeadlineExceeded) {
			t.Fatalf("attempt error = %v, want context.DeadlineExceeded", events.attempts[0].Err)
		}
	})
}

func TestTLSHandshakeRejectsLateVerificationResults(t *testing.T) {
	tests := []struct {
		name              string
		verificationError error
		useTimeout        bool
		wantError         error
		wantOutcome       tcpclient.Outcome
		wantFailure       clientkit.FailureClass
	}{
		{
			name:        "verification success after parent cancellation",
			wantError:   context.Canceled,
			wantOutcome: tcpclient.OutcomeCanceled,
			wantFailure: clientkit.FailureCanceled,
		},
		{
			name:              "verification error after parent cancellation",
			verificationError: errors.New("late verification failure"),
			wantError:         context.Canceled,
			wantOutcome:       tcpclient.OutcomeCanceled,
			wantFailure:       clientkit.FailureCanceled,
		},
		{
			name:        "verification success after handshake timeout",
			useTimeout:  true,
			wantError:   context.DeadlineExceeded,
			wantOutcome: tcpclient.OutcomeTimeout,
			wantFailure: clientkit.FailureTimeout,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			certificate, roots := testCertificate(t, "example.test")
			connection, peer := newCloseSignalingPipe(t)
			serverResults := make(chan error, 1)
			go func() {
				server := tls.Server(peer, &tls.Config{Certificates: []tls.Certificate{certificate}})
				serverResults <- server.Handshake()
				_ = peer.Close()
			}()
			verificationStarted := make(chan struct{})
			releaseVerification := make(chan struct{})
			observer := &tcpObserver{}
			client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
				return connection, nil
			}, func(config *tcpclient.Config) {
				config.Observer = observer
				config.TLS = tcpclient.TLSConfig{
					Enabled: true,
					Config: &tls.Config{
						RootCAs: roots,
						VerifyConnection: func(tls.ConnectionState) error {
							close(verificationStarted)
							<-releaseVerification
							return test.verificationError
						},
					},
				}
				if test.useTimeout {
					config.TLS.HandshakeTimeout = 10 * time.Millisecond
				}
			})

			ctx, cancel := context.WithCancel(context.Background())
			results := make(chan tcpclient.Result, 1)
			go func() {
				results <- client.DialResult(ctx)
			}()
			select {
			case <-verificationStarted:
			case <-time.After(time.Second):
				t.Fatal("TLS verification callback did not start")
			}
			if !test.useTimeout {
				cancel()
			}
			select {
			case <-connection.closedSignal:
			case <-time.After(time.Second):
				t.Fatal("TLS context completion did not close the raw connection")
			}
			close(releaseVerification)

			var result tcpclient.Result
			select {
			case result = <-results:
			case <-time.After(time.Second):
				t.Fatal("DialResult did not return after releasing TLS verification")
			}
			cancel()
			if result.Conn != nil || !errors.Is(result.Err, test.wantError) {
				t.Fatalf("DialResult() = %#v, want %v with no connection", result, test.wantError)
			}
			if result.Outcome != test.wantOutcome || result.FailureClass != test.wantFailure {
				t.Fatalf("classification = (%q, %q), want (%q, %q)", result.Outcome, result.FailureClass, test.wantOutcome, test.wantFailure)
			}
			if !connection.closed.Load() {
				t.Fatal("raw connection remained open after rejected TLS result")
			}

			events := observer.snapshot()
			if len(events.attempts) != 1 || len(events.ends) != 1 {
				t.Fatalf("observer events = (%d attempts, %d ends), want one each", len(events.attempts), len(events.ends))
			}
			if events.attempts[0].Outcome != string(test.wantOutcome) || events.attempts[0].FailureClass != test.wantFailure || !errors.Is(events.attempts[0].Err, test.wantError) {
				t.Fatalf("attempt event = %#v, want context result matching caller", events.attempts[0])
			}
			if events.ends[0].Outcome != string(test.wantOutcome) || events.ends[0].FailureClass != test.wantFailure || !errors.Is(events.ends[0].Err, test.wantError) {
				t.Fatalf("end event = %#v, want context result matching caller", events.ends[0])
			}
		})
	}
}

func TestTLSHandshakeCompletionWinsBeforeLaterCancellation(t *testing.T) {
	certificate, roots := testCertificate(t, "example.test")
	connection, peer := newTrackedPipe(t)
	serverResults := make(chan error, 1)
	go func() {
		server := tls.Server(peer, &tls.Config{Certificates: []tls.Certificate{certificate}})
		serverResults <- server.Handshake()
		_ = peer.Close()
	}()
	client := newCustomTCPClient(t, func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}, func(config *tcpclient.Config) {
		config.TLS = tcpclient.TLSConfig{Enabled: true, Config: &tls.Config{RootCAs: roots}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := client.DialResult(ctx)
	if result.Conn == nil || result.Err != nil || result.Outcome != tcpclient.OutcomeSuccess {
		t.Fatalf("DialResult() = %#v, want successful TLS handshake", result)
	}
	if err := <-serverResults; err != nil {
		t.Fatalf("server handshake error = %v", err)
	}
	cancel()
	if connection.closed.Load() {
		t.Fatal("later parent cancellation closed caller-owned TLS connection")
	}
	_ = result.Conn.Close()
	if !connection.closed.Load() {
		t.Fatal("caller close did not close TLS connection")
	}
}

func tlsPipeDialer(t *testing.T, serverConfig *tls.Config, results chan<- error) tcpclient.DialContextFunc {
	t.Helper()
	return func(context.Context, string, string) (net.Conn, error) {
		clientConnection, serverConnection := net.Pipe()
		t.Cleanup(func() {
			_ = clientConnection.Close()
			_ = serverConnection.Close()
		})
		go func() {
			server := tls.Server(serverConnection, serverConfig.Clone())
			results <- server.Handshake()
			_ = serverConnection.Close()
		}()
		return clientConnection, nil
	}
}

func testCertificate(t *testing.T, serverName string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: serverName},
		DNSNames:     []string{serverName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}),
	)
	if err != nil {
		t.Fatalf("X509KeyPair() error = %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return certificate, roots
}
