package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/opskit"
)

var (
	errRedirectLimitExceeded     = errors.New("stopped after 10 redirects")
	errRequestURLRequired        = errors.New("clientkit: request URL is required")
	errRequestURLMustBeAbsolute  = errors.New("clientkit: request URL must be absolute")
	errRequestURLOpaque          = errors.New("clientkit: request URL must not use opaque form")
	errRequestURLUserInformation = errors.New("clientkit: request URL must not include user information")
	errRequestURLFragment        = errors.New("clientkit: request URL must not include a fragment")
	errRequestHostOverride       = errors.New("clientkit: request Host override is not allowed")
	errRequestOriginMismatch     = errors.New("clientkit: request URL must match configured base URL origin")
	errHTTPTransportNoResponse   = errors.New("clientkit: HTTP transport returned no response and no error")
)

type executionPolicy struct {
	operation          string
	timeout            time.Duration
	attemptTimeout     time.Duration
	retry              RetryConfig
	retryPolicySource  string
	retrySafety        RetrySafety
	responseClassifier safeResponseClassifier
	attributes         []opskit.Attribute
}

// Do executes request using the client's ordinary policy and returns standard
// net/http response/error semantics. HTTP status rejection is represented by a
// non-nil response and nil error. The caller owns any response body and must
// close it; operation observation completes at body EOF or Close.
func (c *Client) Do(request *http.Request) (*http.Response, error) {
	result := c.Execute(request)
	return result.Response, result.Err
}

// Execute executes request and returns Clientkit's detailed classified result.
// It is equivalent to ExecuteWithOptions with zero-value options.
func (c *Client) Execute(request *http.Request) Result {
	return c.ExecuteWithOptions(request, DoOptions{})
}

// ExecuteWithOptions executes request with explicit per-request policy. A non-nil
// options classifier completely overrides the client classifier for this call.
// Accepted responses are never retried; rejected responses may retry under the
// selected retry policy only when RetrySafety authorizes repetition.
// RetrySafetyNever disables retries for this call. Result.Err remains reserved
// for request and transport execution errors, and the caller owns any final
// response body. ExecuteWithOptions takes ownership of a non-nil request body
// and closes it even when validation prevents the first network attempt.
func (c *Client) ExecuteWithOptions(request *http.Request, options DoOptions) Result {
	classifier := normalizeResponseClassifier(nil)
	if c != nil {
		classifier = c.responseClassifier
		if classifier.classifier == nil {
			classifier = normalizeResponseClassifier(nil)
		}
	}
	if options.ResponseClassifier != nil {
		classifier = normalizeResponseClassifier(options.ResponseClassifier)
	}

	if c == nil {
		operation, err := resolveOperationName(options.Operation)
		if err != nil {
			return closeUnattemptedRequestBody(request, Result{
				Outcome:      OutcomeTransportError,
				FailureClass: clientkit.FailurePolicy,
				Err:          err,
			})
		}
		return c.do(request, executionPolicy{
			operation:          operation,
			responseClassifier: classifier,
			retrySafety:        options.RetrySafety,
		})
	}
	timeouts, err := resolveExecutionTimeouts(c.timeout, c.attemptTimeout, options.Timeouts)
	if err != nil {
		return closeUnattemptedRequestBody(request, Result{
			Outcome:      OutcomeTransportError,
			FailureClass: clientkit.FailurePolicy,
			Err:          err,
		})
	}
	retry, err := resolveExecutionRetry(c.retry, options.Retry)
	if err != nil {
		return closeUnattemptedRequestBody(request, Result{
			Outcome:      OutcomeTransportError,
			FailureClass: clientkit.FailurePolicy,
			Err:          err,
		})
	}
	operation, err := resolveOperationName(options.Operation)
	if err != nil {
		return closeUnattemptedRequestBody(request, Result{
			Outcome:      OutcomeTransportError,
			FailureClass: clientkit.FailurePolicy,
			Err:          err,
		})
	}
	return c.do(request, executionPolicy{
		operation:          operation,
		timeout:            timeouts.timeout,
		attemptTimeout:     timeouts.attemptTimeout,
		retry:              retry.config,
		retryPolicySource:  retry.source,
		retrySafety:        options.RetrySafety,
		responseClassifier: classifier,
	})
}

func (c *Client) do(request *http.Request, policy executionPolicy) (result Result) {
	requestAttempted := false
	defer func() {
		if !requestAttempted {
			closeRequestBody(request)
		}
	}()

	if c == nil || c.httpClient == nil || c.baseURL == nil {
		return Result{
			Outcome:      OutcomeTransportError,
			FailureClass: classifyFailure(failureStageConfiguration, nil, nil),
			Err:          errors.New("clientkit: HTTP client is not configured"),
		}
	}
	if request == nil {
		return Result{
			Outcome:      OutcomeTransportError,
			FailureClass: classifyFailure(failureStageRequest, nil, nil),
			Err:          errors.New("clientkit: request is required"),
		}
	}
	if request.URL == nil {
		return Result{
			Outcome:      OutcomeTransportError,
			FailureClass: classifyFailure(failureStageRequest, nil, errRequestURLRequired),
			Err:          errRequestURLRequired,
		}
	}
	if err := c.validateRequestOrigin(request); err != nil {
		return Result{
			Outcome:      OutcomeTransportError,
			FailureClass: classifyFailure(failureStagePolicy, nil, err),
			Err:          err,
		}
	}

	method := normalizedHTTPMethod(request.Method)
	retryAuthorization, err := retryAuthorizationFor(method, policy.retrySafety)
	if err != nil {
		return Result{
			Outcome:      OutcomeTransportError,
			FailureClass: clientkit.FailurePolicy,
			Err:          err,
		}
	}

	operationStartedAt := time.Now()
	responseClassifier := policy.responseClassifier
	if responseClassifier.classifier == nil {
		responseClassifier = normalizeResponseClassifier(nil)
	}
	clientName := c.telemetryClientName()
	observer := c.clientObserver()
	operationContext, operationObservation := observer.StartOperation(request.Context(), clientkit.OperationStartEvent{
		Client:     clientName,
		Protocol:   ProtocolHTTP,
		Operation:  policy.operation,
		StartedAt:  operationStartedAt.UTC(),
		Attributes: httpExecutionAttributes(method, 0, policy.attributes),
	})
	request = request.Clone(operationContext)
	finishOnReturn := true
	finishOperation := func(final Result) {
		endedAt := time.Now()
		attributes := httpExecutionAttributes(method, final.StatusCode, policy.attributes)
		operationObservation.End(operationContext, clientkit.OperationEndEvent{
			Client:       clientName,
			Protocol:     ProtocolHTTP,
			Operation:    policy.operation,
			StartedAt:    operationStartedAt.UTC(),
			EndedAt:      endedAt.UTC(),
			Duration:     endedAt.Sub(operationStartedAt),
			Attempts:     len(final.Attempts),
			Outcome:      string(final.Outcome),
			Succeeded:    final.Outcome == OutcomeSuccess,
			FailureClass: final.FailureClass,
			Err:          final.Err,
			Attributes:   attributes,
		})
	}
	defer func() {
		if finishOnReturn {
			finishOperation(result)
		}
	}()

	maxAttempts := policy.retry.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	totalCtx := request.Context()
	totalCancel := func() {}
	if policy.timeout > 0 {
		totalCtx, totalCancel = context.WithTimeout(totalCtx, policy.timeout)
	}
	cancelTotalOnReturn := true
	defer func() {
		if cancelTotalOnReturn {
			totalCancel()
		}
	}()

	const maxInitialAttemptCapacity = 8
	attemptCapacity := maxAttempts
	if attemptCapacity > maxInitialAttemptCapacity {
		attemptCapacity = maxInitialAttemptCapacity
	}
	attempts := make([]Attempt, 0, attemptCapacity)
	httpClient := c.executionHTTPClient()
	propagator := c.Propagator()

	for attemptNumber := 1; attemptNumber <= maxAttempts; attemptNumber++ {
		if err := totalCtx.Err(); err != nil {
			failureClass := classifyFailure(failureStageTransport, nil, err)
			return Result{
				Outcome:      classifyOutcome(nil, err),
				FailureClass: failureClass,
				StartedAt:    operationStartedAt,
				Duration:     time.Since(operationStartedAt),
				Attempts:     attempts,
				Err:          err,
			}
		}

		attemptCtx := totalCtx
		attemptCancel := func() {}
		if policy.attemptTimeout > 0 {
			attemptCtx, attemptCancel = context.WithTimeout(attemptCtx, policy.attemptTimeout)
		}

		attemptRequest, err := requestForAttempt(request, attemptCtx, attemptNumber)
		if err != nil {
			attemptCancel()
			return Result{
				Outcome:      OutcomeTransportError,
				FailureClass: classifyFailure(failureStageRequest, nil, err),
				StartedAt:    operationStartedAt,
				Duration:     time.Since(operationStartedAt),
				Attempts:     attempts,
				Err:          err,
			}
		}

		if attemptRequest.Header == nil {
			attemptRequest.Header = make(http.Header)
		}

		startedAt := time.Now()
		propagator.Inject(attemptRequest.Context(), attemptRequest.Header)
		requestAttempted = true
		response, err := httpClient.Do(attemptRequest)
		endedAt := time.Now()
		duration := endedAt.Sub(startedAt)
		if response == nil && err == nil {
			err = errHTTPTransportNoResponse
		}
		outcome := classifyOutcome(response, err)
		failureClass := classifyFailure(failureStageTransport, response, err)
		responseClassification := responseNotClassified
		if err == nil && response != nil {
			responseClassification = responseClassifier.classify(response)
			switch responseClassification {
			case responseClassifiedAccepted:
				outcome = OutcomeSuccess
				failureClass = clientkit.FailureNone
			case responseClassifiedRejected:
				outcome = OutcomeHTTPError
				failureClass = clientkit.FailureRemoteResponse
			default:
				outcome = OutcomeHTTPError
				failureClass = clientkit.FailurePolicy
			}
		}
		statusCode := 0
		if response != nil {
			statusCode = response.StatusCode
		}

		attempts = append(attempts, Attempt{
			Number:       attemptNumber,
			Outcome:      outcome,
			FailureClass: failureClass,
			StatusCode:   statusCode,
			StartedAt:    startedAt,
			Duration:     duration,
			Err:          err,
		})
		attemptAttributes := httpExecutionAttributes(attemptRequest.Method, statusCode, policy.attributes)
		observer.ObserveAttempt(attemptCtx, clientkit.AttemptEvent{
			Client:       clientName,
			Protocol:     ProtocolHTTP,
			Operation:    policy.operation,
			Number:       attemptNumber,
			StartedAt:    startedAt.UTC(),
			EndedAt:      endedAt.UTC(),
			Duration:     duration,
			Outcome:      string(outcome),
			Succeeded:    outcome == OutcomeSuccess,
			FailureClass: failureClass,
			Err:          err,
			Attributes:   attemptAttributes,
		})

		retry := outcome != OutcomeSuccess &&
			attemptNumber < maxAttempts &&
			totalCtx.Err() == nil &&
			!isRequestPolicyError(err) &&
			failureClass != clientkit.FailurePolicy &&
			retryAuthorization.allowsRetry() &&
			requestBodyIsReplayable(request) &&
			policy.retry.shouldRetry(method, outcome, statusCode)

		if !retry {
			finalResult := Result{
				Outcome:      outcome,
				FailureClass: failureClass,
				StatusCode:   statusCode,
				Response:     response,
				StartedAt:    operationStartedAt,
				Duration:     time.Since(operationStartedAt),
				Attempts:     attempts,
				Err:          err,
			}
			release := func() {
				attemptCancel()
				totalCancel()
			}
			observedResult := finalResult
			observedResult.Response = nil
			// A returned body extends the operation lifecycle beyond response headers.
			// Completion and Clientkit-owned contexts transfer to the body wrapper so
			// the caller's EOF or Close is represented exactly once.
			if err == nil && attachResponseLifecycle(response, func(bodyErr error) {
				defer release()
				if bodyErr != nil {
					observedResult.Outcome = classifyOutcome(nil, bodyErr)
					observedResult.FailureClass = classifyFailure(failureStageTransport, nil, bodyErr)
					observedResult.Err = bodyErr
				}
				finishOperation(observedResult)
			}) {
				cancelTotalOnReturn = false
				finishOnReturn = false
			} else {
				attemptCancel()
			}
			return finalResult
		}

		policyDelay := policy.retry.retryDelay(attemptNumber)
		retryDelay := policyDelay
		retryDelaySource := "policy"
		if outcome == OutcomeHTTPError && policy.retry.RespectRetryAfter {
			if serverDelay, ok := retryAfterDelay(response, endedAt); ok {
				if serverDelay > policy.retry.MaxRetryAfter {
					serverDelay = policy.retry.MaxRetryAfter
				}
				if serverDelay > policyDelay {
					retryDelay = serverDelay
					retryDelaySource = "retry_after"
				}
			}
		}
		closeResponse(response)
		attemptCancel()
		retryAttributes := httpExecutionAttributes(attemptRequest.Method, statusCode, policy.attributes)
		retryAttributes = append(retryAttributes, opskit.Attr("http.retry_delay_source", retryDelaySource))
		retryAttributes = append(retryAttributes, opskit.Attr("http.retry_authorization", string(retryAuthorization)))
		if policy.retryPolicySource != "" {
			retryAttributes = append(retryAttributes, opskit.Attr("http.retry_policy_source", policy.retryPolicySource))
		}
		observer.ObserveRetry(totalCtx, clientkit.RetryEvent{
			Client:       clientName,
			Protocol:     ProtocolHTTP,
			Operation:    policy.operation,
			AfterAttempt: attemptNumber,
			At:           time.Now().UTC(),
			Delay:        retryDelay,
			Cause:        string(outcome),
			FailureClass: failureClass,
			Attributes:   retryAttributes,
		})
		if err := waitForRetryDelay(totalCtx, retryDelay); err != nil {
			failureClass := classifyFailure(failureStageTransport, nil, err)
			return Result{
				Outcome:      classifyOutcome(nil, err),
				FailureClass: failureClass,
				StartedAt:    operationStartedAt,
				Duration:     time.Since(operationStartedAt),
				Attempts:     attempts,
				Err:          err,
			}
		}
	}

	return Result{
		Outcome:      OutcomeTransportError,
		FailureClass: clientkit.FailureTransport,
		StartedAt:    operationStartedAt,
		Duration:     time.Since(operationStartedAt),
		Attempts:     attempts,
		Err:          errors.New("clientkit: HTTP execution ended without an attempt"),
	}
}

func (c *Client) executionHTTPClient() *http.Client {
	// Compose redirect and origin policy on a copy so execution never mutates a
	// caller-owned http.Client or the Clientkit client's immutable configuration.
	executionClient := *c.httpClient
	configuredCheckRedirect := executionClient.CheckRedirect

	executionClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if configuredCheckRedirect != nil {
			if err := configuredCheckRedirect(request, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errRedirectLimitExceeded
		}

		return c.validateRequestOrigin(request)
	}

	return &executionClient
}

func (c *Client) validateRequestOrigin(request *http.Request) error {
	if c == nil || c.baseURL == nil {
		return errors.New("clientkit: HTTP client is not configured")
	}
	if request == nil || request.URL == nil {
		return errRequestURLRequired
	}
	if !request.URL.IsAbs() || request.URL.Host == "" {
		return errRequestURLMustBeAbsolute
	}
	if request.URL.Opaque != "" {
		return errRequestURLOpaque
	}
	if request.URL.User != nil {
		return errRequestURLUserInformation
	}
	if request.URL.Fragment != "" || request.URL.RawFragment != "" {
		return errRequestURLFragment
	}
	if !c.allowHostOverride && request.Host != "" && !strings.EqualFold(request.Host, request.URL.Host) {
		return errRequestHostOverride
	}
	if c.allowCrossOrigin {
		return nil
	}

	schemeMatches := strings.EqualFold(request.URL.Scheme, c.baseURL.Scheme)
	hostnameMatches := strings.EqualFold(request.URL.Hostname(), c.baseURL.Hostname())
	requestPort := effectiveURLPort(request.URL.Scheme, request.URL.Port())
	basePort := effectiveURLPort(c.baseURL.Scheme, c.baseURL.Port())
	if !schemeMatches || !hostnameMatches || requestPort != basePort {
		return errRequestOriginMismatch
	}

	return nil
}

func isRequestPolicyError(err error) bool {
	return errors.Is(err, errRedirectLimitExceeded) ||
		errors.Is(err, errRequestURLRequired) ||
		errors.Is(err, errRequestURLMustBeAbsolute) ||
		errors.Is(err, errRequestURLOpaque) ||
		errors.Is(err, errRequestURLUserInformation) ||
		errors.Is(err, errRequestURLFragment) ||
		errors.Is(err, errRequestHostOverride) ||
		errors.Is(err, errRequestOriginMismatch)
}

func requestBodyIsReplayable(request *http.Request) bool {
	if request == nil || request.Body == nil || request.Body == http.NoBody {
		return true
	}

	return request.GetBody != nil
}

func effectiveURLPort(scheme string, explicitPort string) string {
	if explicitPort != "" {
		return explicitPort
	}

	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

type observedResponseBody struct {
	io.ReadCloser
	once     sync.Once
	complete func(error)
}

func (b *observedResponseBody) Read(buffer []byte) (int, error) {
	read, err := b.ReadCloser.Read(buffer)
	if err != nil {
		if errors.Is(err, io.EOF) {
			b.finish(nil)
		} else {
			b.finish(err)
		}
	}
	return read, err
}

func (b *observedResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish(err)
	return err
}

func (b *observedResponseBody) finish(err error) {
	b.once.Do(func() {
		b.complete(err)
	})
}

func attachResponseLifecycle(response *http.Response, complete func(error)) bool {
	if response == nil || response.Body == nil || response.Body == http.NoBody {
		return false
	}

	response.Body = &observedResponseBody{ReadCloser: response.Body, complete: complete}
	return true
}

func closeResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}

	// A bounded drain improves connection reuse for small responses without
	// reading into a known-large intermediate body. Unknown-length responses are
	// still bounded because they may be small and reusable.
	const drainLimit = 32 << 10
	if response.ContentLength < 0 || response.ContentLength <= drainLimit {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, drainLimit))
	}
	_ = response.Body.Close()
}

func closeUnattemptedRequestBody(request *http.Request, result Result) Result {
	closeRequestBody(request)
	return result
}

func closeRequestBody(request *http.Request) {
	if request == nil || request.Body == nil || request.Body == http.NoBody {
		return
	}
	_ = request.Body.Close()
}

func requestForAttempt(request *http.Request, ctx context.Context, attemptNumber int) (*http.Request, error) {
	if request == nil {
		return nil, errors.New("clientkit: request is required")
	}
	if ctx == nil {
		return nil, errors.New("clientkit: context is required")
	}
	if attemptNumber < 1 {
		return nil, errors.New("clientkit: attempt number must be at least 1")
	}

	// Clone protects caller-owned headers, URL values, and context while keeping
	// net/http's standard first-attempt body ownership semantics.
	attemptRequest := request.Clone(ctx)
	if attemptNumber == 1 || request.Body == nil || request.Body == http.NoBody {
		return attemptRequest, nil
	}

	if request.GetBody == nil {
		return nil, errors.New("clientkit: request body cannot be replayed for retry")
	}

	// Recreate retry bodies only when the next attempt begins; no open body is
	// retained during backoff or Retry-After waiting.
	body, err := request.GetBody()
	if err != nil {
		if body != nil {
			_ = body.Close()
		}
		return nil, fmt.Errorf("clientkit: recreate request body: %w", err)
	}
	if body == nil {
		return nil, errors.New("clientkit: recreate request body: GetBody returned nil")
	}

	attemptRequest.Body = body
	return attemptRequest, nil
}

func classifyOutcome(response *http.Response, err error) Outcome {
	if isRedirectPolicyFailure(response, err) {
		return OutcomeTransportError
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return OutcomeCanceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return OutcomeTimeout
		}

		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return OutcomeTimeout
		}

		return OutcomeTransportError
	}
	if response == nil {
		return OutcomeTransportError
	}

	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return OutcomeSuccess
	}

	return OutcomeHTTPError
}
