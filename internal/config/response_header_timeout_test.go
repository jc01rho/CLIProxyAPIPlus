package config

import (
	"math"
	"testing"
	"time"
)

// TestSDKConfigResponseHeaderTimeout pins the contract for the upstream
// response-header wait: a positive value in seconds is honored, while an
// unset, zero or negative value falls back to the 120 second default.
func TestSDKConfigResponseHeaderTimeout(t *testing.T) {
	tests := []struct {
		name string
		cfg  *SDKConfig
		want time.Duration
	}{
		{name: "nil receiver uses default", cfg: nil, want: 120 * time.Second},
		{name: "unset uses default", cfg: &SDKConfig{}, want: 120 * time.Second},
		{name: "negative uses default", cfg: &SDKConfig{ResponseHeaderTimeoutSeconds: -5}, want: 120 * time.Second},
		{name: "positive seconds convert", cfg: &SDKConfig{ResponseHeaderTimeoutSeconds: 300}, want: 300 * time.Second},
		{name: "overflowing seconds saturate", cfg: &SDKConfig{ResponseHeaderTimeoutSeconds: math.MaxInt}, want: time.Duration(math.MaxInt64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ResponseHeaderTimeout(); got != tt.want {
				t.Fatalf("ResponseHeaderTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseConfigBytesRequestsResponseHeaderTimeout ensures both the canonical
// v8 layout and the pre-v8 flat layout map onto the same field.
func TestParseConfigBytesRequestsResponseHeaderTimeout(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want time.Duration
	}{
		{name: "v8 layout", yaml: "requests:\n  response-header-timeout-seconds: 90\n", want: 90 * time.Second},
		{name: "legacy flat layout", yaml: "response-header-timeout-seconds: 75\n", want: 75 * time.Second},
		{name: "omitted uses default", yaml: "requests:\n  proxy-url: \"\"\n", want: 120 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tt.yaml))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if got := cfg.ResponseHeaderTimeout(); got != tt.want {
				t.Fatalf("ResponseHeaderTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}
