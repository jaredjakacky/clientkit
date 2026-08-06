// Package tcpclient establishes ordinary plaintext or TLS-over-TCP connections
// while preserving Clientkit identity, readiness, cached health, and
// observability. It returns net.Conn values and does not implement an
// application protocol, pool connections, or retry automatically. It does not
// implement clientkit.IdleConnectionCloser because successful net.Conn values
// are caller-owned and are neither pooled nor tracked by Clientkit.
//
// # Defaults and TLS
//
// The package uses production-oriented dial-timeout and TCP-keepalive defaults.
// Every default can be overridden or explicitly disabled. Plaintext is the
// default. Opt-in TLS without a caller configuration verifies certificates and
// requires TLS 1.2 or newer. A supplied tls.Config is cloned and treated as a
// complete policy override; setting InsecureSkipVerify disables standard
// certificate verification and should be done only deliberately. TCP dialing
// and TLS handshaking have separate timeout protections.
//
// A nil Observer selects Clientkit's automatic OpenTelemetry observer, which
// captures the global providers during Client construction. A non-nil Observer
// completely replaces that default. Use clientkit.NopObserver to disable
// observation or clientkit.MultiObserver to compose observers explicitly. The
// application owns OpenTelemetry SDK and provider lifecycle.
//
// # Custom dialing
//
// A custom DialContext replaces only the built-in net.Dialer. Clientkit passes
// it the normalized network and address, applies the configured dial timeout,
// and performs the optional TLS stage afterward. The custom function owns socket
// options, so KeepAlive and DisableKeepAlive cannot be configured with it. It
// may take complete ownership of TLS by returning an already-secured connection
// while TLSConfig.Enabled remains false. Clientkit does not detect existing TLS
// connections and does not implement STARTTLS or apply application-level read or
// write deadlines. Telemetry labels this opaque security policy as custom rather
// than assuming the returned connection is plaintext.
//
// # Connection ownership
//
// Successful plaintext and TLS connections are returned through net.Conn, are
// caller-owned, and must be closed by the caller. Clientkit does not track or
// serialize access to them; concurrency guarantees are those of the concrete
// connection. Clientkit-controlled attributes and operational snapshots exclude
// server names, addresses, certificates, and raw TLS errors. The default OTel
// observer also omits raw errors; explicitly enabled error-detail adapters or
// custom observers may expose them.
//
// # Example
//
// A TLS client with an optional connect-and-close health check can be
// constructed as follows:
//
//	client, err := tcpclient.New(tcpclient.Config{
//		Config: clientkit.Config{
//			Name: "postgres-wire",
//		},
//		Address: "postgres.internal:5432",
//		TLS: tcpclient.TLSConfig{
//			Enabled: true,
//		},
//		Check: tcpclient.CheckConfig{
//			Enabled: true,
//		},
//	})
//	if err != nil {
//		// handle error
//	}
//
//	connection, err := client.Dial(ctx)
//	if err != nil {
//		// handle error
//	}
//	defer connection.Close()
//
// Caller-created trust policy is supplied without Clientkit loading files or
// parsing certificates:
//
//	customTrust := tcpclient.TLSConfig{
//		Enabled: true,
//		Config: &tls.Config{
//			RootCAs: roots,
//		},
//	}
package tcpclient
