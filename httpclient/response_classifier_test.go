package httpclient_test

import (
	"net/http"
	"testing"

	clientkit "github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
)

func TestDefaultResponseClassifier(t *testing.T) {
	classifier := httpclient.DefaultResponseClassifier()
	for _, statusCode := range []int{http.StatusOK, http.StatusNoContent, 299} {
		if got := classifier.Classify(&http.Response{StatusCode: statusCode}); got != httpclient.ResponseAccepted {
			t.Errorf("Classify(%d) = %q, want accepted", statusCode, got)
		}
	}
	for _, statusCode := range []int{0, 199, 300, http.StatusNotFound, http.StatusInternalServerError} {
		if got := classifier.Classify(&http.Response{StatusCode: statusCode}); got != httpclient.ResponseRejected {
			t.Errorf("Classify(%d) = %q, want rejected", statusCode, got)
		}
	}
	if got := classifier.Classify(nil); got != httpclient.ResponseRejected {
		t.Fatalf("Classify(nil) = %q, want rejected", got)
	}
}

func TestResponseClassifierConstructors(t *testing.T) {
	exact, err := httpclient.AcceptStatus(http.StatusNotFound)
	if err != nil {
		t.Fatalf("AcceptStatus() error = %v", err)
	}
	assertClassification(t, exact, http.StatusNotFound, httpclient.ResponseAccepted)
	assertClassification(t, exact, http.StatusOK, httpclient.ResponseRejected)

	class, err := httpclient.AcceptStatusClass(4)
	if err != nil {
		t.Fatalf("AcceptStatusClass() error = %v", err)
	}
	assertClassification(t, class, 400, httpclient.ResponseAccepted)
	assertClassification(t, class, 499, httpclient.ResponseAccepted)
	assertClassification(t, class, 500, httpclient.ResponseRejected)

	any, err := httpclient.AcceptAnyStatus(http.StatusOK, http.StatusNotFound)
	if err != nil {
		t.Fatalf("AcceptAnyStatus() error = %v", err)
	}
	assertClassification(t, any, http.StatusOK, httpclient.ResponseAccepted)
	assertClassification(t, any, http.StatusNotFound, httpclient.ResponseAccepted)
	assertClassification(t, any, http.StatusCreated, httpclient.ResponseRejected)
	statuses := []int{http.StatusAccepted, http.StatusTeapot}
	cloned, err := httpclient.AcceptAnyStatus(statuses...)
	if err != nil {
		t.Fatalf("AcceptAnyStatus() clone error = %v", err)
	}
	statuses[0] = http.StatusInternalServerError
	assertClassification(t, cloned, http.StatusAccepted, httpclient.ResponseAccepted)
	assertClassification(t, cloned, http.StatusInternalServerError, httpclient.ResponseRejected)

	rangeClassifier, err := httpclient.AcceptStatusRange(201, 203)
	if err != nil {
		t.Fatalf("AcceptStatusRange() error = %v", err)
	}
	assertClassification(t, rangeClassifier, 201, httpclient.ResponseAccepted)
	assertClassification(t, rangeClassifier, 203, httpclient.ResponseAccepted)
	assertClassification(t, rangeClassifier, 204, httpclient.ResponseRejected)

	invalid := []struct {
		name string
		call func() error
	}{
		{name: "status below range", call: func() error { _, err := httpclient.AcceptStatus(99); return err }},
		{name: "status above range", call: func() error { _, err := httpclient.AcceptStatus(600); return err }},
		{name: "class below range", call: func() error { _, err := httpclient.AcceptStatusClass(0); return err }},
		{name: "class above range", call: func() error { _, err := httpclient.AcceptStatusClass(6); return err }},
		{name: "empty status set", call: func() error { _, err := httpclient.AcceptAnyStatus(); return err }},
		{name: "set status below range", call: func() error { _, err := httpclient.AcceptAnyStatus(99); return err }},
		{name: "set status above range", call: func() error { _, err := httpclient.AcceptAnyStatus(600); return err }},
		{name: "duplicate set status", call: func() error { _, err := httpclient.AcceptAnyStatus(200, 200); return err }},
		{name: "reversed range", call: func() error { _, err := httpclient.AcceptStatusRange(300, 200); return err }},
		{name: "lower bound below range", call: func() error { _, err := httpclient.AcceptStatusRange(99, 200); return err }},
		{name: "upper bound above range", call: func() error { _, err := httpclient.AcceptStatusRange(200, 600); return err }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("classifier constructor error = nil")
			}
		})
	}
}

func TestSafeResponseClassifier(t *testing.T) {
	if got := httpclient.ResponseClassifierFunc(nil).Classify(&http.Response{StatusCode: http.StatusOK}); got != httpclient.ResponseRejected {
		t.Fatalf("nil ResponseClassifierFunc.Classify() = %q, want rejected", got)
	}
	tests := []struct {
		name       string
		classifier httpclient.ResponseClassifier
	}{
		{name: "nil", classifier: nil},
		{name: "typed nil", classifier: httpclient.ResponseClassifierFunc(nil)},
		{name: "panic", classifier: httpclient.ResponseClassifierFunc(func(*http.Response) httpclient.ResponseDisposition { panic("classify") })},
		{name: "unsupported", classifier: httpclient.ResponseClassifierFunc(func(*http.Response) httpclient.ResponseDisposition { return "custom" })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classifier := httpclient.SafeResponseClassifier(test.classifier)
			got := classifier.Classify(&http.Response{StatusCode: http.StatusNoContent})
			if test.name == "nil" {
				if got != httpclient.ResponseAccepted {
					t.Fatalf("Classify() = %q, want default acceptance", got)
				}
				return
			}
			if got != httpclient.ResponseRejected {
				t.Fatalf("Classify() = %q, want safe rejection", got)
			}
		})
	}

	nested := httpclient.SafeResponseClassifier(httpclient.SafeResponseClassifier(httpclient.DefaultResponseClassifier()))
	if got := nested.Classify(&http.Response{StatusCode: http.StatusNoContent}); got != httpclient.ResponseAccepted {
		t.Fatalf("nested SafeResponseClassifier.Classify() = %q, want accepted", got)
	}
}

func TestResponseClassifierControlsExecution(t *testing.T) {
	client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	}, httpclient.Config{
		ResponseClassifier: httpclient.ResponseClassifierFunc(func(response *http.Response) httpclient.ResponseDisposition {
			if response.StatusCode == http.StatusNotFound {
				return httpclient.ResponseAccepted
			}
			return httpclient.ResponseRejected
		}),
	})
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
	result := client.Execute(request)
	if result.Outcome != httpclient.OutcomeSuccess || result.FailureClass != clientkit.FailureNone || result.Err != nil {
		t.Fatalf("Execute() = %#v, want classifier-accepted success", result)
	}

	request, _ = http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
	result = client.ExecuteWithOptions(request, httpclient.ExecuteOptions{ResponseClassifier: httpclient.DefaultResponseClassifier()})
	if result.Outcome != httpclient.OutcomeResponseRejected || result.FailureClass != clientkit.FailureRemoteResponse || result.Err != nil {
		t.Fatalf("ExecuteWithOptions() = %#v, want per-call rejection", result)
	}
}

func TestResponseClassifierFailuresUseExecutionError(t *testing.T) {
	tests := []struct {
		name       string
		classifier httpclient.ResponseClassifier
	}{
		{
			name: "panic",
			classifier: httpclient.ResponseClassifierFunc(func(*http.Response) httpclient.ResponseDisposition {
				panic("classify")
			}),
		},
		{
			name: "unsupported disposition",
			classifier: httpclient.ResponseClassifierFunc(func(*http.Response) httpclient.ResponseDisposition {
				return "custom"
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := newHTTPTestClient(t, func(request *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
			}, httpclient.Config{
				ResponseClassifier: test.classifier,
				Retry: httpclient.RetryConfig{
					MaxAttempts:     2,
					Methods:         []string{http.MethodGet},
					TransportErrors: httpclient.TransportRetryAll,
				},
			})
			request, _ := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
			result := client.Execute(request)
			if result.Outcome != httpclient.OutcomeExecutionError || result.FailureClass != clientkit.FailurePolicy || result.Err != nil || result.Response == nil || calls != 1 || len(result.Attempts) != 1 {
				t.Fatalf("Execute() = %#v with %d calls, want one non-retried policy execution error", result, calls)
			}
			if err := result.Response.Body.Close(); err != nil {
				t.Fatalf("Body.Close() error = %v", err)
			}
		})
	}
}

func assertClassification(t *testing.T, classifier httpclient.ResponseClassifier, statusCode int, want httpclient.ResponseDisposition) {
	t.Helper()
	if got := classifier.Classify(&http.Response{StatusCode: statusCode}); got != want {
		t.Fatalf("Classify(%d) = %q, want %q", statusCode, got, want)
	}
}
