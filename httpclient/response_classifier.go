package httpclient

import (
	"errors"
	"fmt"
	"net/http"
)

// ResponseDisposition is Clientkit's closed classification vocabulary for a
// completed HTTP response. Classification never consumes, decodes, or closes
// the response; application code continues to own the final response.
type ResponseDisposition string

const (
	// ResponseAccepted means Clientkit treats the completed response as a
	// successful HTTP operation.
	ResponseAccepted ResponseDisposition = "accepted"
	// ResponseRejected means Clientkit treats the completed response as an HTTP
	// error outcome.
	ResponseRejected ResponseDisposition = "rejected"
)

// ResponseClassifier classifies completed HTTP responses. Implementations may
// be called concurrently and must return quickly. They must not retain the
// response, read, close, or replace its body, or emit response metadata through
// Clientkit telemetry attributes. They may inspect bounded metadata such as the
// status code and headers when application policy requires it.
type ResponseClassifier interface {
	// Classify returns whether response is accepted or rejected.
	Classify(*http.Response) ResponseDisposition
}

// ResponseClassifierFunc adapts a function to ResponseClassifier.
type ResponseClassifierFunc func(*http.Response) ResponseDisposition

// Classify invokes fn. A nil function rejects the response.
func (fn ResponseClassifierFunc) Classify(response *http.Response) ResponseDisposition {
	if fn == nil {
		return ResponseRejected
	}
	return fn(response)
}

type responseClassification uint8

const (
	responseNotClassified responseClassification = iota
	responseClassifiedAccepted
	responseClassifiedRejected
	responseClassificationInvalid
)

type safeResponseClassifier struct {
	classifier ResponseClassifier
}

// DefaultResponseClassifier returns an immutable classifier that accepts HTTP
// status codes from 200 through 299 and rejects every other status.
func DefaultResponseClassifier() ResponseClassifier {
	return statusRangeResponseClassifier{minimum: http.StatusOK, maximum: http.StatusMultipleChoices - 1}
}

// AcceptStatus returns an immutable classifier accepting exactly statusCode.
func AcceptStatus(statusCode int) (ResponseClassifier, error) {
	if err := validateResponseStatus(statusCode); err != nil {
		return nil, err
	}
	return exactStatusResponseClassifier{statusCode: statusCode}, nil
}

// AcceptStatusClass returns an immutable classifier accepting HTTP status class
// 1 through 5. Class 2 accepts status codes 200 through 299.
func AcceptStatusClass(class int) (ResponseClassifier, error) {
	if class < 1 || class > 5 {
		return nil, fmt.Errorf("clientkit: response status class %d must be between 1 and 5", class)
	}
	return statusRangeResponseClassifier{minimum: class * 100, maximum: class*100 + 99}, nil
}

// AcceptAnyStatus returns an immutable classifier accepting any supplied status
// code. It clones the supplied values and rejects empty, invalid, or duplicate
// status sets.
func AcceptAnyStatus(statusCodes ...int) (ResponseClassifier, error) {
	if len(statusCodes) == 0 {
		return nil, errors.New("clientkit: response statuses are required")
	}
	statuses := append([]int(nil), statusCodes...)
	seen := make(map[int]struct{}, len(statuses))
	for _, statusCode := range statuses {
		if err := validateResponseStatus(statusCode); err != nil {
			return nil, err
		}
		if _, exists := seen[statusCode]; exists {
			return nil, fmt.Errorf("clientkit: duplicate response status %d", statusCode)
		}
		seen[statusCode] = struct{}{}
	}
	return anyStatusResponseClassifier{statusCodes: statuses}, nil
}

// AcceptStatusRange returns an immutable classifier accepting every status from
// minimum through maximum, inclusive.
func AcceptStatusRange(minimum, maximum int) (ResponseClassifier, error) {
	if err := validateResponseStatus(minimum); err != nil {
		return nil, fmt.Errorf("clientkit: response status range minimum: %w", err)
	}
	if err := validateResponseStatus(maximum); err != nil {
		return nil, fmt.Errorf("clientkit: response status range maximum: %w", err)
	}
	if minimum > maximum {
		return nil, errors.New("clientkit: response status range minimum must not exceed maximum")
	}
	return statusRangeResponseClassifier{minimum: minimum, maximum: maximum}, nil
}

// SafeResponseClassifier contains classifier panics and unsupported results.
// Nil selects DefaultResponseClassifier. During Client execution, either
// condition becomes OutcomeExecutionError with FailurePolicy and is never retried.
func SafeResponseClassifier(classifier ResponseClassifier) ResponseClassifier {
	return normalizeResponseClassifier(classifier)
}

func normalizeResponseClassifier(classifier ResponseClassifier) safeResponseClassifier {
	if safe, ok := classifier.(safeResponseClassifier); ok {
		return safe
	}
	if classifier == nil {
		classifier = DefaultResponseClassifier()
	}
	return safeResponseClassifier{classifier: classifier}
}

func (classifier safeResponseClassifier) Classify(response *http.Response) ResponseDisposition {
	if classifier.classify(response) == responseClassifiedAccepted {
		return ResponseAccepted
	}
	return ResponseRejected
}

func (classifier safeResponseClassifier) classify(response *http.Response) (classification responseClassification) {
	classification = responseClassificationInvalid
	if fn, ok := classifier.classifier.(ResponseClassifierFunc); ok && fn == nil {
		return classification
	}
	defer func() {
		if recover() != nil {
			classification = responseClassificationInvalid
		}
	}()

	switch classifier.classifier.Classify(response) {
	case ResponseAccepted:
		return responseClassifiedAccepted
	case ResponseRejected:
		return responseClassifiedRejected
	default:
		return responseClassificationInvalid
	}
}

type exactStatusResponseClassifier struct {
	statusCode int
}

func (classifier exactStatusResponseClassifier) Classify(response *http.Response) ResponseDisposition {
	if response != nil && response.StatusCode == classifier.statusCode {
		return ResponseAccepted
	}
	return ResponseRejected
}

type statusRangeResponseClassifier struct {
	minimum int
	maximum int
}

func (classifier statusRangeResponseClassifier) Classify(response *http.Response) ResponseDisposition {
	if response != nil && response.StatusCode >= classifier.minimum && response.StatusCode <= classifier.maximum {
		return ResponseAccepted
	}
	return ResponseRejected
}

type anyStatusResponseClassifier struct {
	statusCodes []int
}

func (classifier anyStatusResponseClassifier) Classify(response *http.Response) ResponseDisposition {
	if response != nil {
		for _, statusCode := range classifier.statusCodes {
			if response.StatusCode == statusCode {
				return ResponseAccepted
			}
		}
	}
	return ResponseRejected
}

func validateResponseStatus(statusCode int) error {
	if statusCode < 100 || statusCode > 599 {
		return fmt.Errorf("clientkit: response status %d must be between 100 and 599", statusCode)
	}
	return nil
}
