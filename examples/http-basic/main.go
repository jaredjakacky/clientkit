package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func main() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ready\n")
	}))
	defer server.Close()

	client, err := httpclient.New(httpclient.Config{
		Config:  clientkit.Config{Name: "example-api"},
		BaseURL: server.URL + "/api/",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.CloseIdleConnections()

	request, err := client.NewRequest(context.Background(), http.MethodGet, "status", nil)
	if err != nil {
		log.Fatal(err)
	}

	// Do is the short path when ordinary net/http response and error semantics
	// are enough. Use Execute when the caller needs Clientkit's detailed Result.
	response, err := client.Do(request)
	if err != nil {
		log.Fatal(err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("status=%s body=%q\n", response.Status, string(body))
}
