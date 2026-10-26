package teamshare

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Usage is the token breakdown of one request.
type Usage struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// Price is a per-million-token USD price.
type Price struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

type priceRule struct {
	match string
	price Price
}

// defaultPriceRules is the public Anthropic API price list in USD per million
// tokens, matched as substrings of the lower-cased model name; the longest
// match wins, so "opus-4-1" beats the generic "opus" rule.
var defaultPriceRules = []priceRule{
	{"opus-4-1", priceFromInput(15, 75)},
	{"opus-4-0", priceFromInput(15, 75)},
	{"claude-opus-4-2025", priceFromInput(15, 75)},
	{"claude-3-opus", priceFromInput(15, 75)},
	{"opus", priceFromInput(5, 25)},
	{"sonnet", priceFromInput(3, 15)},
	{"haiku-3-5", priceFromInput(0.8, 4)},
	{"3-5-haiku", priceFromInput(0.8, 4)},
	{"claude-3-haiku", priceFromInput(0.25, 1.25)},
	{"haiku", priceFromInput(1, 5)},
}

var fallbackPrice = priceFromInput(3, 15)

func priceFromInput(in, out float64) Price {
	return Price{Input: in, Output: out, CacheRead: in * 0.1, CacheWrite: in * 1.25}
}

// PriceTable converts token usage to API-equivalent USD.
type PriceTable struct {
	overrides []priceRule
}

// NewPriceTable builds a table whose config overrides take precedence over the built-in list.
func NewPriceTable(overrides map[string]config.TeamSharePrice) *PriceTable {
	table := &PriceTable{}
	for key, p := range overrides {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		price := Price{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite}
		if price.CacheRead == 0 {
			price.CacheRead = price.Input * 0.1
		}
		if price.CacheWrite == 0 {
			price.CacheWrite = price.Input * 1.25
		}
		table.overrides = append(table.overrides, priceRule{match: key, price: price})
	}
	return table
}

func longestMatch(rules []priceRule, model string) (Price, bool) {
	var best priceRule
	found := false
	for _, rule := range rules {
		if strings.Contains(model, rule.match) && (!found || len(rule.match) > len(best.match)) {
			best, found = rule, true
		}
	}
	return best.price, found
}

// Lookup returns the price for a model name.
func (t *PriceTable) Lookup(model string) Price {
	model = strings.ToLower(strings.TrimSpace(model))
	if t != nil {
		if price, ok := longestMatch(t.overrides, model); ok {
			return price
		}
	}
	if price, ok := longestMatch(defaultPriceRules, model); ok {
		return price
	}
	return fallbackPrice
}

// CostUSD returns the API-equivalent cost of usage on model.
func (t *PriceTable) CostUSD(model string, u Usage) float64 {
	p := t.Lookup(model)
	const perMillion = 1_000_000.0
	return (float64(u.Input)*p.Input +
		float64(u.Output)*p.Output +
		float64(u.CacheRead)*p.CacheRead +
		float64(u.CacheWrite)*p.CacheWrite) / perMillion
}
