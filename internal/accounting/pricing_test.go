package accounting

import (
	"testing"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

func TestParseUSDPerMillion(t *testing.T) {
	tests := map[string]uint64{
		"0":        0,
		"1":        1_000_000,
		"1.25":     1_250_000,
		"0.000001": 1,
		".5":       500_000,
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			got, err := ParseUSDPerMillion(input)
			if err != nil || got != want {
				t.Fatalf("ParseUSDPerMillion() = %d, %v; want %d", got, err, want)
			}
		})
	}
	for _, input := range []string{"", "-1", "1.0000001", "one"} {
		t.Run("invalid "+input, func(t *testing.T) {
			if _, err := ParseUSDPerMillion(input); err == nil {
				t.Fatal("ParseUSDPerMillion() error = nil")
			}
		})
	}
}

func TestEstimateMicroUSD(t *testing.T) {
	inputRate, outputRate := uint64(2_000_000), uint64(4_000_000)
	rates := Rates{InputMicroUSDPerMillion: &inputRate, OutputMicroUSDPerMillion: &outputRate}
	tests := []struct {
		name        string
		usage       *openai.Usage
		want        uint64
		wantPresent bool
	}{
		{name: "input only", usage: openai.NewUsage(1_000_000, 0, 1_000_000), want: 2_000_000, wantPresent: true},
		{name: "output only", usage: openai.NewUsage(0, 1_000_000, 1_000_000), want: 4_000_000, wantPresent: true},
		{name: "combined", usage: openai.NewUsage(500_000, 250_000, 750_000), want: 2_000_000, wantPresent: true},
		{name: "large", usage: openai.NewUsage(1_000_000_000_000, 0, 1_000_000_000_000), want: 2_000_000_000_000, wantPresent: true},
		{name: "missing usage", wantPresent: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, present := EstimateMicroUSD(test.usage, rates)
			if got != test.want || present != test.wantPresent {
				t.Fatalf("EstimateMicroUSD() = %d, %v; want %d, %v", got, present, test.want, test.wantPresent)
			}
		})
	}
}

func TestEstimateMicroUSDRoundsHalfUpAndRequiresPricing(t *testing.T) {
	inputRate, outputRate := uint64(1), uint64(0)
	cost, ok := EstimateMicroUSD(openai.NewUsage(500_000, 0, 500_000), Rates{
		InputMicroUSDPerMillion: &inputRate, OutputMicroUSDPerMillion: &outputRate,
	})
	if !ok || cost != 1 {
		t.Fatalf("rounded cost = %d, %v", cost, ok)
	}
	if cost, ok := EstimateMicroUSD(openai.NewUsage(1_000_000, 0, 1_000_000), Rates{}); ok || cost != 0 {
		t.Fatalf("missing-price cost = %d, %v", cost, ok)
	}
}
