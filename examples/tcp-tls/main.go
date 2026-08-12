package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/tcpclient"
)

func main() {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello\n")
	}))
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())

	client, err := tcpclient.New(tcpclient.Config{
		Config:  clientkit.Config{Name: "local-tls"},
		Address: server.Listener.Addr().String(),
		TLS: tcpclient.TLSConfig{
			Enabled: true,
			Config: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    roots,
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	conn, err := client.Dial(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	// Clientkit established and verified the connection. The caller owns its
	// protocol exchange and must close the returned net.Conn.
	defer conn.Close()

	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n"); err != nil {
		log.Fatal(err)
	}

	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(status)
}
