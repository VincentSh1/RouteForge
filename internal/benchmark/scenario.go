package benchmark

import (
	"fmt"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

type Mode string

const (
	ModeNonStreaming Mode = "non_streaming"
	ModeStreaming    Mode = "streaming"
)

type State string

const (
	StateCold State = "cold"
	StateWarm State = "warm"
)

type StreamFailurePoint string

const (
	FailureBeforeCommit StreamFailurePoint = "before_commit"
	FailureAfterCommit  StreamFailurePoint = "after_commit"
)

type ProviderSpec struct {
	Name  string
	Model string
	Rates accounting.Rates
}

// ProviderCondition describes one provider's counterfactual behavior for one
// abstract request. A blank ErrorKind and Canceled=false represent success.
type ProviderCondition struct {
	CompletionLatency time.Duration
	TTFC              time.Duration
	StreamDuration    time.Duration
	ErrorKind         provider.ErrorKind
	Canceled          bool
	StreamFailure     StreamFailurePoint
	Usage             *openai.Usage
}

type Request struct {
	ID         string
	Conditions map[string]ProviderCondition
}

type Scenario struct {
	Name            string
	Mode            Mode
	Providers       []ProviderSpec
	Warmup          []Request
	Requests        []Request
	InterRequestGap time.Duration
	Circuit         gateway.CircuitConfig
	Routing         gateway.RoutingConfig
}

func (s Scenario) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("scenario name is required")
	}
	if s.Mode != ModeNonStreaming && s.Mode != ModeStreaming {
		return fmt.Errorf("scenario mode must be non_streaming or streaming")
	}
	if len(s.Providers) == 0 {
		return fmt.Errorf("scenario requires providers")
	}
	if len(s.Requests) == 0 {
		return fmt.Errorf("scenario requires measured requests")
	}
	if s.InterRequestGap < 0 {
		return fmt.Errorf("scenario inter-request gap must not be negative")
	}

	providers := make(map[string]struct{}, len(s.Providers))
	for _, spec := range s.Providers {
		if spec.Name == "" || spec.Model == "" {
			return fmt.Errorf("scenario provider name and model are required")
		}
		if _, exists := providers[spec.Name]; exists {
			return fmt.Errorf("scenario provider names must be unique")
		}
		providers[spec.Name] = struct{}{}
	}

	requests := append(append([]Request(nil), s.Warmup...), s.Requests...)
	ids := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		if request.ID == "" {
			return fmt.Errorf("scenario request ID is required")
		}
		if _, exists := ids[request.ID]; exists {
			return fmt.Errorf("scenario request IDs must be unique")
		}
		ids[request.ID] = struct{}{}
		for providerName := range providers {
			condition, ok := request.Conditions[providerName]
			if !ok {
				return fmt.Errorf("request %q lacks provider %q", request.ID, providerName)
			}
			if err := validateCondition(s.Mode, condition); err != nil {
				return fmt.Errorf("request %q provider %q: %w", request.ID, providerName, err)
			}
		}
	}
	return nil
}

func validateCondition(mode Mode, condition ProviderCondition) error {
	if condition.CompletionLatency < 0 || condition.TTFC < 0 || condition.StreamDuration < 0 {
		return fmt.Errorf("durations must not be negative")
	}
	if condition.Canceled && condition.ErrorKind != "" {
		return fmt.Errorf("cancellation and provider error are mutually exclusive")
	}
	if condition.ErrorKind != "" && !validErrorKind(condition.ErrorKind) {
		return fmt.Errorf("unsupported provider error kind %q", condition.ErrorKind)
	}
	failed := condition.Canceled || condition.ErrorKind != ""
	if mode == ModeNonStreaming {
		if condition.StreamFailure != "" {
			return fmt.Errorf("non-streaming condition has a stream failure point")
		}
		return nil
	}
	if condition.StreamDuration < condition.TTFC {
		return fmt.Errorf("stream duration must not be less than TTFC")
	}
	if failed && condition.StreamFailure == "" {
		return fmt.Errorf("stream failure point is required for a failed stream")
	}
	if !failed && condition.StreamFailure != "" {
		return fmt.Errorf("successful stream has a failure point")
	}
	if condition.StreamFailure != "" && condition.StreamFailure != FailureBeforeCommit && condition.StreamFailure != FailureAfterCommit {
		return fmt.Errorf("unsupported stream failure point %q", condition.StreamFailure)
	}
	return nil
}

func validErrorKind(kind provider.ErrorKind) bool {
	switch kind {
	case provider.ErrorTimeout, provider.ErrorUnavailable, provider.ErrorRateLimited,
		provider.ErrorInvalidRequest, provider.ErrorInternal:
		return true
	default:
		return false
	}
}
