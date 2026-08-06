package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"strings"
)

const (
	// DefaultContextHeaderMaxValueBytes is the default byte limit for one
	// context-derived header value.
	DefaultContextHeaderMaxValueBytes = 256
	// DefaultRequestIDHeader is a common request-ID header convention, not an
	// IETF standard.
	DefaultRequestIDHeader = "X-Request-ID"
	// DefaultCorrelationIDHeader is a common correlation-ID header convention,
	// not an IETF standard.
	DefaultCorrelationIDHeader = "X-Correlation-ID"
)

// HeaderValueProvider reads one outbound header value from context. Providers
// are called once for each actual network attempt and may be called
// concurrently. They must be concurrency-safe, return quickly, avoid indefinite
// blocking, and not retain the context. Providers should return a stable value
// already stored in context rather than generating a new identifier per call.
type HeaderValueProvider interface {
	// Value returns a context-derived value and whether it is available.
	Value(context.Context) (string, bool)
}

// HeaderValueProviderFunc adapts a function to HeaderValueProvider.
type HeaderValueProviderFunc func(context.Context) (string, bool)

// Value invokes fn. A nil function reports no available value.
func (fn HeaderValueProviderFunc) Value(ctx context.Context) (string, bool) {
	if fn == nil {
		return "", false
	}
	return fn(ctx)
}

// ExistingHeaderPolicy controls how context-derived metadata interacts with a
// header already present on an attempt-specific request.
type ExistingHeaderPolicy string

const (
	// PreserveExistingHeader leaves every explicitly present header unchanged,
	// including headers with an empty slice or empty value. It is the default.
	PreserveExistingHeader ExistingHeaderPolicy = ""
	// OverwriteExistingHeader replaces all existing values using Header.Set.
	OverwriteExistingHeader ExistingHeaderPolicy = "overwrite"
)

// ContextHeaderBinding configures one context-derived outbound HTTP header.
// Values are limited to DefaultContextHeaderMaxValueBytes unless overridden or
// explicitly disabled. Invalid dynamic values are silently omitted. Clientkit
// does not define context keys, generate values, or expose values through
// telemetry, health, inspection, snapshots, or errors. Custom header
// propagation may have security consequences, which remain the caller's
// responsibility.
type ContextHeaderBinding struct {
	// Header is the required HTTP field name. It must not contain surrounding
	// whitespace and is stored in canonical MIME form.
	Header string
	// Provider reads the value from the attempt context.
	Provider HeaderValueProvider
	// ExistingPolicy preserves an existing header by default. Overwriting must
	// be selected explicitly with OverwriteExistingHeader.
	ExistingPolicy ExistingHeaderPolicy
	// MaxValueBytes overrides DefaultContextHeaderMaxValueBytes when positive.
	MaxValueBytes int
	// DisableValueLimit disables Clientkit's byte limit. It cannot be combined
	// with a positive MaxValueBytes.
	DisableValueLimit bool
}

// RequestMetadataConfig configures conventional request-ID and correlation-ID
// propagation from application-owned context values. Header names are
// configurable; Clientkit defines no context keys, generates no identifiers,
// and emits no propagated values through telemetry.
type RequestMetadataConfig struct {
	// RequestID provides the request ID when available.
	RequestID HeaderValueProvider
	// RequestIDHeader overrides DefaultRequestIDHeader. It requires RequestID.
	RequestIDHeader string
	// CorrelationID provides the correlation ID when available.
	CorrelationID HeaderValueProvider
	// CorrelationIDHeader overrides DefaultCorrelationIDHeader. It requires
	// CorrelationID.
	CorrelationIDHeader string
	// ExistingPolicy applies to both metadata headers and preserves existing
	// values by default.
	ExistingPolicy ExistingHeaderPolicy
	// MaxValueBytes applies one shared positive byte limit to both values. Zero
	// selects DefaultContextHeaderMaxValueBytes.
	MaxValueBytes int
	// DisableValueLimit disables Clientkit's value limit for both headers. It
	// cannot be combined with a positive MaxValueBytes.
	DisableValueLimit bool
}

type normalizedContextHeaderBinding struct {
	header         string
	provider       HeaderValueProvider
	existingPolicy ExistingHeaderPolicy
	maxValueBytes  int
}

type contextHeaderPropagator struct {
	bindings []normalizedContextHeaderBinding
}

// NewContextHeaderPropagator validates immutable context-derived header
// bindings. Existing headers are preserved and values are limited to 256 bytes
// by default. Providers are invoked independently once per actual attempt;
// panics and unusable values omit only that binding and never fail the request.
func NewContextHeaderPropagator(bindings ...ContextHeaderBinding) (HeaderPropagator, error) {
	if len(bindings) == 0 {
		return NopHeaderPropagator{}, nil
	}

	normalized := make([]normalizedContextHeaderBinding, 0, len(bindings))
	headers := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		normalizedBinding, err := normalizeContextHeaderBinding(binding)
		if err != nil {
			return nil, err
		}
		if _, exists := headers[normalizedBinding.header]; exists {
			return nil, fmt.Errorf("clientkit: duplicate context header %q", normalizedBinding.header)
		}
		headers[normalizedBinding.header] = struct{}{}
		normalized = append(normalized, normalizedBinding)
	}

	return contextHeaderPropagator{bindings: normalized}, nil
}

// NewRequestMetadataPropagator constructs request-ID and correlation-ID
// propagation through NewContextHeaderPropagator. Empty enabled header names
// use the documented conventions. A zero configuration returns a no-op
// propagator, and explicit names without corresponding providers are invalid.
func NewRequestMetadataPropagator(cfg RequestMetadataConfig) (HeaderPropagator, error) {
	if cfg.RequestID == nil && cfg.RequestIDHeader != "" {
		return nil, errors.New("clientkit: request ID header requires a provider")
	}
	if cfg.CorrelationID == nil && cfg.CorrelationIDHeader != "" {
		return nil, errors.New("clientkit: correlation ID header requires a provider")
	}
	if err := validateExistingHeaderPolicy(cfg.ExistingPolicy); err != nil {
		return nil, err
	}
	if _, err := normalizeContextHeaderValueLimit(cfg.MaxValueBytes, cfg.DisableValueLimit); err != nil {
		return nil, err
	}

	bindings := make([]ContextHeaderBinding, 0, 2)
	if cfg.RequestID != nil {
		header := cfg.RequestIDHeader
		if header == "" {
			header = DefaultRequestIDHeader
		}
		bindings = append(bindings, ContextHeaderBinding{
			Header:            header,
			Provider:          cfg.RequestID,
			ExistingPolicy:    cfg.ExistingPolicy,
			MaxValueBytes:     cfg.MaxValueBytes,
			DisableValueLimit: cfg.DisableValueLimit,
		})
	}
	if cfg.CorrelationID != nil {
		header := cfg.CorrelationIDHeader
		if header == "" {
			header = DefaultCorrelationIDHeader
		}
		bindings = append(bindings, ContextHeaderBinding{
			Header:            header,
			Provider:          cfg.CorrelationID,
			ExistingPolicy:    cfg.ExistingPolicy,
			MaxValueBytes:     cfg.MaxValueBytes,
			DisableValueLimit: cfg.DisableValueLimit,
		})
	}

	return NewContextHeaderPropagator(bindings...)
}

func (p contextHeaderPropagator) Inject(ctx context.Context, headers http.Header) {
	if headers == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for _, binding := range p.bindings {
		preserveExisting := binding.existingPolicy == PreserveExistingHeader && headerIsPresent(headers, binding.header)
		value, ok := contextHeaderValue(binding.provider, ctx)
		if preserveExisting || !ok || !validContextHeaderValue(value, binding.maxValueBytes) {
			continue
		}
		setContextHeader(headers, binding.header, value)
	}
}

func normalizeContextHeaderBinding(binding ContextHeaderBinding) (normalizedContextHeaderBinding, error) {
	header := strings.TrimSpace(binding.Header)
	if header == "" {
		return normalizedContextHeaderBinding{}, errors.New("clientkit: context header name is required")
	}
	if header != binding.Header {
		return normalizedContextHeaderBinding{}, fmt.Errorf("clientkit: context header name %q must not contain surrounding whitespace", binding.Header)
	}
	if !validHTTPFieldName(header) {
		return normalizedContextHeaderBinding{}, fmt.Errorf("clientkit: invalid context header name %q", header)
	}
	header = textproto.CanonicalMIMEHeaderKey(header)

	if binding.Provider == nil {
		return normalizedContextHeaderBinding{}, fmt.Errorf("clientkit: context header %q provider is required", header)
	}
	if fn, ok := binding.Provider.(HeaderValueProviderFunc); ok && fn == nil {
		return normalizedContextHeaderBinding{}, fmt.Errorf("clientkit: context header %q provider is required", header)
	}
	if err := validateExistingHeaderPolicy(binding.ExistingPolicy); err != nil {
		return normalizedContextHeaderBinding{}, err
	}
	maxValueBytes, err := normalizeContextHeaderValueLimit(binding.MaxValueBytes, binding.DisableValueLimit)
	if err != nil {
		return normalizedContextHeaderBinding{}, err
	}

	return normalizedContextHeaderBinding{
		header:         header,
		provider:       binding.Provider,
		existingPolicy: binding.ExistingPolicy,
		maxValueBytes:  maxValueBytes,
	}, nil
}

func validateExistingHeaderPolicy(policy ExistingHeaderPolicy) error {
	switch policy {
	case PreserveExistingHeader, OverwriteExistingHeader:
		return nil
	default:
		return fmt.Errorf("clientkit: invalid existing header policy %q", policy)
	}
}

func normalizeContextHeaderValueLimit(maxValueBytes int, disabled bool) (int, error) {
	if maxValueBytes < 0 {
		return 0, errors.New("clientkit: context header max value bytes must not be negative")
	}
	if disabled && maxValueBytes > 0 {
		return 0, errors.New("clientkit: context header max value bytes cannot be set when the limit is disabled")
	}
	if disabled {
		return 0, nil
	}
	if maxValueBytes == 0 {
		return DefaultContextHeaderMaxValueBytes, nil
	}
	return maxValueBytes, nil
}

func validHTTPFieldName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		if !httpTokenByte(name[index]) {
			return false
		}
	}
	return true
}

func httpTokenByte(value byte) bool {
	if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' {
		return true
	}
	switch value {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

func contextHeaderValue(provider HeaderValueProvider, ctx context.Context) (value string, ok bool) {
	defer func() {
		if recover() != nil {
			value = ""
			ok = false
		}
	}()
	return provider.Value(ctx)
}

func validContextHeaderValue(value string, maxValueBytes int) bool {
	if value == "" || maxValueBytes > 0 && len(value) > maxValueBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '\t' || character >= 0x20 && character != 0x7f {
			continue
		}
		return false
	}
	return true
}

func headerIsPresent(headers http.Header, name string) bool {
	for existingName := range headers {
		if strings.EqualFold(existingName, name) {
			return true
		}
	}
	return false
}

func setContextHeader(headers http.Header, name string, value string) {
	for existingName := range headers {
		if existingName != name && strings.EqualFold(existingName, name) {
			delete(headers, existingName)
		}
	}
	headers.Set(name, value)
}
