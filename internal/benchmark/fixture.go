package benchmark

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	benchmarkfixtures "github.com/VincentSh1/RouteForge/benchmarks"
	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

// MaxFixtureSize bounds decoded scenario input to one mebibyte.
const MaxFixtureSize = 1 << 20

var BuiltInScenarioNames = []string{"stable", "degradation", "rate_limit", "streaming", "cold_start"}

type fixtureV1 struct {
	Version         int               `json:"version"`
	Name            string            `json:"name"`
	Mode            Mode              `json:"mode"`
	Providers       []providerFixture `json:"providers"`
	Warmup          []requestFixture  `json:"warmup"`
	Requests        []requestFixture  `json:"requests"`
	InterRequestGap *string           `json:"inter_request_gap"`
	Circuit         circuitFixture    `json:"circuit"`
	Routing         routingFixture    `json:"routing"`
}

type providerFixture struct {
	Name                     string  `json:"name"`
	Model                    string  `json:"model"`
	InputPriceUSDPerMillion  *string `json:"input_price_usd_per_million"`
	OutputPriceUSDPerMillion *string `json:"output_price_usd_per_million"`
}

type requestFixture struct {
	ID        string                      `json:"id"`
	Providers map[string]conditionFixture `json:"providers"`
}

type conditionFixture struct {
	Outcome           string  `json:"outcome"`
	CompletionLatency *string `json:"completion_latency"`
	TTFC              *string `json:"ttfc"`
	StreamDuration    *string `json:"stream_duration"`
	StreamFailure     string  `json:"stream_failure_point"`
	InputTokens       *uint64 `json:"input_tokens"`
	OutputTokens      *uint64 `json:"output_tokens"`
}

type circuitFixture struct {
	FailureThreshold int     `json:"failure_threshold"`
	OpenDuration     *string `json:"open_duration"`
}

type routingFixture struct {
	MinSamples                   int     `json:"min_samples"`
	SampleMaxAge                 *string `json:"sample_max_age"`
	ExplorationInterval          int     `json:"exploration_interval"`
	MaxLatencyOverFastestPercent *uint64 `json:"max_latency_over_fastest_percent"`
}

func BuiltInScenario(name string) (Scenario, error) {
	if !isBuiltInScenario(name) {
		return Scenario{}, fmt.Errorf("unknown built-in scenario %q", name)
	}
	path := "v1/" + name + ".json"
	file, err := benchmarkfixtures.FS.Open(path)
	if err != nil {
		return Scenario{}, fmt.Errorf("benchmarks/%s: embedded fixture unavailable", path)
	}
	defer file.Close()

	scenario, err := DecodeScenario("benchmarks/"+path, file)
	if err != nil {
		return Scenario{}, err
	}
	if scenario.Name != name {
		return Scenario{}, fmt.Errorf("benchmarks/%s: scenario name must be %q", path, name)
	}
	return scenario, nil
}

func LoadScenarioFile(path string) (Scenario, error) {
	label := fixtureLabel(path)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Scenario{}, fmt.Errorf("%s: fixture not found", label)
		}
		return Scenario{}, fmt.Errorf("%s: cannot open fixture", label)
	}
	defer file.Close()
	return DecodeScenario(label, file)
}

func DecodeScenario(source string, reader io.Reader) (Scenario, error) {
	label := fixtureLabel(source)
	data, err := io.ReadAll(io.LimitReader(reader, MaxFixtureSize+1))
	if err != nil {
		return Scenario{}, fmt.Errorf("%s: cannot read fixture", label)
	}
	if len(data) > MaxFixtureSize {
		return Scenario{}, fmt.Errorf("%s: fixture exceeds %d bytes", label, MaxFixtureSize)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var fixture fixtureV1
	if err := decoder.Decode(&fixture); err != nil {
		return Scenario{}, fmt.Errorf("%s: decode fixture: %w", label, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Scenario{}, fmt.Errorf("%s: fixture must contain one JSON object", label)
		}
		return Scenario{}, fmt.Errorf("%s: decode fixture: %w", label, err)
	}
	if fixture.Version != 1 {
		return Scenario{}, fmt.Errorf("%s: unsupported scenario version %d", label, fixture.Version)
	}

	scenario, err := fixture.toScenario()
	if err != nil {
		return Scenario{}, fmt.Errorf("%s: %w", label, err)
	}
	if err := scenario.Validate(); err != nil {
		return Scenario{}, fmt.Errorf("%s: %w", label, err)
	}
	return scenario, nil
}

func (f fixtureV1) toScenario() (Scenario, error) {
	if strings.TrimSpace(f.Name) == "" {
		return Scenario{}, fmt.Errorf("scenario name is required")
	}
	if f.Mode != ModeNonStreaming && f.Mode != ModeStreaming {
		return Scenario{}, fmt.Errorf("scenario mode must be non_streaming or streaming")
	}
	if len(f.Providers) == 0 {
		return Scenario{}, fmt.Errorf("scenario requires providers")
	}
	gap, err := requiredDuration("inter_request_gap", f.InterRequestGap, true)
	if err != nil {
		return Scenario{}, err
	}
	openDuration, err := requiredDuration("circuit.open_duration", f.Circuit.OpenDuration, false)
	if err != nil {
		return Scenario{}, err
	}
	sampleMaxAge, err := requiredDuration("routing.sample_max_age", f.Routing.SampleMaxAge, false)
	if err != nil {
		return Scenario{}, err
	}
	if f.Circuit.FailureThreshold <= 0 {
		return Scenario{}, fmt.Errorf("circuit.failure_threshold must be positive")
	}
	if f.Routing.MinSamples <= 0 {
		return Scenario{}, fmt.Errorf("routing.min_samples must be positive")
	}
	if f.Routing.ExplorationInterval <= 0 {
		return Scenario{}, fmt.Errorf("routing.exploration_interval must be positive")
	}
	if f.Routing.MaxLatencyOverFastestPercent == nil {
		return Scenario{}, fmt.Errorf("routing.max_latency_over_fastest_percent is required")
	}

	scenario := Scenario{
		Version: f.Version, Name: f.Name, Mode: f.Mode,
		InterRequestGap: gap,
		Circuit: gateway.CircuitConfig{
			FailureThreshold: f.Circuit.FailureThreshold, OpenDuration: openDuration,
		},
		Routing: gateway.RoutingConfig{
			MinSamples: f.Routing.MinSamples, SampleMaxAge: sampleMaxAge,
			ExplorationInterval:          f.Routing.ExplorationInterval,
			MaxLatencyOverFastestPercent: cloneUint64(f.Routing.MaxLatencyOverFastestPercent),
		},
	}

	scenario.Providers = make([]ProviderSpec, len(f.Providers))
	providerNames := make(map[string]struct{}, len(f.Providers))
	for i, item := range f.Providers {
		if item.Name == "" || item.Model == "" {
			return Scenario{}, fmt.Errorf("providers[%d] name and model are required", i)
		}
		if _, exists := providerNames[item.Name]; exists {
			return Scenario{}, fmt.Errorf("providers[%d] duplicates provider %q", i, item.Name)
		}
		providerNames[item.Name] = struct{}{}
		rates, err := fixtureRates(item)
		if err != nil {
			return Scenario{}, fmt.Errorf("providers[%d] %q: %w", i, item.Name, err)
		}
		scenario.Providers[i] = ProviderSpec{Name: item.Name, Model: item.Model, Rates: rates}
	}

	convertRequests := func(section string, fixtures []requestFixture) ([]Request, error) {
		requests := make([]Request, len(fixtures))
		for i, request := range fixtures {
			conditions := make(map[string]ProviderCondition, len(request.Providers))
			for _, spec := range scenario.Providers {
				condition, ok := request.Providers[spec.Name]
				if !ok {
					return nil, fmt.Errorf("%s[%d] request %q lacks provider %q", section, i, request.ID, spec.Name)
				}
				converted, err := condition.toCondition(f.Mode)
				if err != nil {
					return nil, fmt.Errorf("%s[%d] request %q provider %q: %w", section, i, request.ID, spec.Name, err)
				}
				conditions[spec.Name] = converted
			}
			for providerName := range request.Providers {
				if _, ok := providerNames[providerName]; !ok {
					return nil, fmt.Errorf("%s[%d] request %q contains unknown provider %q", section, i, request.ID, providerName)
				}
			}
			requests[i] = Request{ID: request.ID, Conditions: conditions}
		}
		return requests, nil
	}
	if scenario.Warmup, err = convertRequests("warmup", f.Warmup); err != nil {
		return Scenario{}, err
	}
	if scenario.Requests, err = convertRequests("requests", f.Requests); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}

func (f conditionFixture) toCondition(mode Mode) (ProviderCondition, error) {
	condition := ProviderCondition{StreamFailure: StreamFailurePoint(f.StreamFailure)}
	switch f.Outcome {
	case "success":
	case "cancellation":
		condition.Canceled = true
	case string(provider.ErrorTimeout), string(provider.ErrorUnavailable), string(provider.ErrorRateLimited),
		string(provider.ErrorInvalidRequest), string(provider.ErrorInternal):
		condition.ErrorKind = provider.ErrorKind(f.Outcome)
	default:
		return ProviderCondition{}, fmt.Errorf("unknown outcome %q", f.Outcome)
	}

	if mode == ModeNonStreaming {
		if f.TTFC != nil || f.StreamDuration != nil || f.StreamFailure != "" {
			return ProviderCondition{}, fmt.Errorf("non-streaming result contains streaming fields")
		}
		latency, err := requiredDuration("completion_latency", f.CompletionLatency, true)
		if err != nil {
			return ProviderCondition{}, err
		}
		condition.CompletionLatency = latency
	} else {
		if f.CompletionLatency != nil {
			return ProviderCondition{}, fmt.Errorf("streaming result contains completion_latency")
		}
		duration, err := requiredDuration("stream_duration", f.StreamDuration, true)
		if err != nil {
			return ProviderCondition{}, err
		}
		condition.StreamDuration = duration
		if f.StreamFailure == string(FailureBeforeCommit) {
			if f.TTFC != nil {
				return ProviderCondition{}, fmt.Errorf("before_commit failure must not define ttfc")
			}
		} else {
			ttfc, err := requiredDuration("ttfc", f.TTFC, true)
			if err != nil {
				return ProviderCondition{}, err
			}
			condition.TTFC = ttfc
		}
	}

	if f.InputTokens != nil || f.OutputTokens != nil {
		usage := &openai.Usage{InputTokens: cloneUint64(f.InputTokens), OutputTokens: cloneUint64(f.OutputTokens)}
		if f.InputTokens != nil && f.OutputTokens != nil {
			if *f.InputTokens > math.MaxUint64-*f.OutputTokens {
				return ProviderCondition{}, fmt.Errorf("input_tokens plus output_tokens is out of range")
			}
			total := *f.InputTokens + *f.OutputTokens
			usage.TotalTokens = &total
		}
		condition.Usage = usage
	}
	if err := validateCondition(mode, condition); err != nil {
		return ProviderCondition{}, err
	}
	return condition, nil
}

func fixtureRates(f providerFixture) (accounting.Rates, error) {
	var rates accounting.Rates
	for _, item := range []struct {
		name   string
		value  *string
		target **uint64
	}{
		{name: "input_price_usd_per_million", value: f.InputPriceUSDPerMillion, target: &rates.InputMicroUSDPerMillion},
		{name: "output_price_usd_per_million", value: f.OutputPriceUSDPerMillion, target: &rates.OutputMicroUSDPerMillion},
	} {
		if item.value == nil {
			continue
		}
		parsed, err := accounting.ParseUSDPerMillion(*item.value)
		if err != nil {
			return accounting.Rates{}, fmt.Errorf("%s: %w", item.name, err)
		}
		*item.target = &parsed
	}
	return rates, nil
}

func requiredDuration(field string, value *string, allowZero bool) (time.Duration, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return 0, fmt.Errorf("%s is required", field)
	}
	duration, err := time.ParseDuration(*value)
	if err != nil || duration < 0 || !allowZero && duration == 0 {
		if allowZero {
			return 0, fmt.Errorf("%s must be a non-negative duration with units", field)
		}
		return 0, fmt.Errorf("%s must be a positive duration with units", field)
	}
	return duration, nil
}

func isBuiltInScenario(name string) bool {
	for _, builtIn := range BuiltInScenarioNames {
		if name == builtIn {
			return true
		}
	}
	return false
}

func fixtureLabel(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Base(path)
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == "." || cleaned == "" {
		return "scenario fixture"
	}
	return cleaned
}
