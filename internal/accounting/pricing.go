package accounting

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

const (
	MicroUSDPerUSD   = uint64(1_000_000)
	TokensPerMillion = uint64(1_000_000)
)

type Key struct {
	Provider string
	Model    string
}

type Rates struct {
	InputMicroUSDPerMillion  *uint64
	OutputMicroUSDPerMillion *uint64
}

type PriceBook map[Key]Rates

func ParseUSDPerMillion(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return 0, errors.New("price must be a non-negative decimal")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" && (len(parts) == 1 || parts[1] == "") {
		return 0, errors.New("price must be a non-negative decimal")
	}
	wholeText := parts[0]
	if wholeText == "" {
		wholeText = "0"
	}
	whole, err := strconv.ParseUint(wholeText, 10, 64)
	if err != nil || whole > math.MaxUint64/MicroUSDPerUSD {
		return 0, errors.New("price is out of range")
	}
	fractionText := ""
	if len(parts) == 2 {
		fractionText = parts[1]
	}
	if len(fractionText) > 6 {
		return 0, errors.New("price supports at most six decimal places")
	}
	for len(fractionText) < 6 {
		fractionText += "0"
	}
	fraction := uint64(0)
	if fractionText != "" {
		fraction, err = strconv.ParseUint(fractionText, 10, 64)
		if err != nil {
			return 0, errors.New("price must be a non-negative decimal")
		}
	}
	base := whole * MicroUSDPerUSD
	if base > math.MaxUint64-fraction {
		return 0, errors.New("price is out of range")
	}
	return base + fraction, nil
}

func EstimateMicroUSD(providerUsage *openai.Usage, rates Rates) (uint64, bool) {
	if providerUsage == nil || providerUsage.InputTokens == nil || providerUsage.OutputTokens == nil ||
		providerUsage.TotalTokens == nil || rates.InputMicroUSDPerMillion == nil || rates.OutputMicroUSDPerMillion == nil {
		return 0, false
	}
	input, output, total := *providerUsage.InputTokens, *providerUsage.OutputTokens, *providerUsage.TotalTokens
	if input > math.MaxUint64-output || total != input+output {
		return 0, false
	}
	inputCost, ok := checkedMultiply(input, *rates.InputMicroUSDPerMillion)
	if !ok {
		return 0, false
	}
	outputCost, ok := checkedMultiply(output, *rates.OutputMicroUSDPerMillion)
	if !ok || inputCost > math.MaxUint64-outputCost {
		return 0, false
	}
	numerator := inputCost + outputCost
	half := TokensPerMillion / 2
	if numerator > math.MaxUint64-half {
		return 0, false
	}
	return (numerator + half) / TokensPerMillion, true
}

func checkedMultiply(left, right uint64) (uint64, bool) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, false
	}
	return left * right, true
}

func ClonePriceBook(prices PriceBook) PriceBook {
	cloned := make(PriceBook, len(prices))
	for key, rates := range prices {
		cloned[key] = Rates{
			InputMicroUSDPerMillion:  cloneUint64(rates.InputMicroUSDPerMillion),
			OutputMicroUSDPerMillion: cloneUint64(rates.OutputMicroUSDPerMillion),
		}
	}
	return cloned
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
