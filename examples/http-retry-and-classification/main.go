package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

const (
	operationUnsafeCreate httpclient.OperationName = "orders.unsafe_create"
	operationSafeCreate   httpclient.OperationName = "orders.safe_create"
)

func main() {
	var unsafeCalls atomic.Int32
	var safeCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/unsafe":
			unsafeCalls.Add(1)
			http.Error(w, "try again", http.StatusServiceUnavailable)
		case "/safe":
			if safeCalls.Add(1) == 1 {
				http.Error(w, "try again", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusConflict) // The idempotency key was already applied.
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	classifier, err := httpclient.AcceptAnyStatus(
		http.StatusCreated,
		http.StatusConflict,
	)
	if err != nil {
		log.Fatal(err)
	}

	retry := httpclient.DefaultRetryConfig()
	retry.MaxAttempts = 2
	retry.Methods = append(retry.Methods, http.MethodPost)
	retry.Backoff = 0
	retry.MaxBackoff = 0
	retry.Jitter = 0

	client, err := httpclient.New(httpclient.Config{
		Config:             clientkit.Config{Name: "orders"},
		BaseURL:            server.URL + "/",
		ResponseClassifier: classifier,
		Retry:              retry,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.CloseIdleConnections()

	unsafeRequest, err := client.NewRequest(
		context.Background(),
		http.MethodPost,
		"unsafe",
		strings.NewReader(`{"sku":"unsafe"}`),
	)
	if err != nil {
		log.Fatal(err)
	}
	unsafeResult := client.ExecuteWithOptions(unsafeRequest, httpclient.ExecuteOptions{
		Operation: operationUnsafeCreate,
	})
	closeResponse(unsafeResult.Response)
	if unsafeResult.Err != nil {
		log.Fatal(unsafeResult.Err)
	}
	if unsafeResult.Outcome != httpclient.OutcomeResponseRejected ||
		unsafeResult.FailureClass != clientkit.FailureRemoteResponse ||
		len(unsafeResult.Attempts) != 1 ||
		unsafeCalls.Load() != 1 {
		log.Fatalf("unsafe POST was not contained to one rejected attempt: %+v", unsafeResult)
	}

	// Policy and a replayable body are insufficient: default retry safety does
	// not authorize POST, so this rejected response uses one attempt.
	fmt.Printf(
		"unsafe outcome=%s failure_class=%q attempts=%d calls=%d\n",
		unsafeResult.Outcome,
		unsafeResult.FailureClass,
		len(unsafeResult.Attempts),
		unsafeCalls.Load(),
	)

	safeRequest, err := client.NewRequest(
		context.Background(),
		http.MethodPost,
		"safe",
		strings.NewReader(`{"sku":"safe"}`),
	)
	if err != nil {
		log.Fatal(err)
	}
	// Clientkit does not create or validate idempotency keys. The application
	// owns this key and the server-side deduplication policy behind it.
	safeRequest.Header.Set("Idempotency-Key", "example-order-1")

	safeResult := client.ExecuteWithOptions(safeRequest, httpclient.ExecuteOptions{
		Operation:   operationSafeCreate,
		RetrySafety: httpclient.RetrySafetyIdempotent,
	})
	closeResponse(safeResult.Response)
	if safeResult.Err != nil {
		log.Fatal(safeResult.Err)
	}
	if safeResult.Outcome != httpclient.OutcomeSuccess ||
		safeResult.FailureClass != clientkit.FailureNone ||
		safeResult.StatusCode != http.StatusConflict ||
		len(safeResult.Attempts) != 2 ||
		safeCalls.Load() != 2 {
		log.Fatalf("idempotent POST did not complete after one retry: %+v", safeResult)
	}

	fmt.Printf(
		"safe outcome=%s failure_class=%q status=%d attempts=%d calls=%d\n",
		safeResult.Outcome,
		safeResult.FailureClass,
		safeResult.StatusCode,
		len(safeResult.Attempts),
		safeCalls.Load(),
	)
}

func closeResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}
