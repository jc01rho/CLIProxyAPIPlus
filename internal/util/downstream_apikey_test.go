package util

import (
	"net/http"
	"testing"
)

func TestExtractDownstreamAPIKey(t *testing.T) {
	t.Parallel()

	const secret = "sk-secret-alpha"
	tests := []struct {
		name   string
		header http.Header
		want   string
	}{
		{name: "nil header", header: nil, want: ""},
		{name: "empty header", header: http.Header{}, want: ""},
		{name: "authorization bearer", header: http.Header{"Authorization": {"Bearer " + secret}}, want: "Bearer(" + secret + ")"},
		{name: "x-api-key", header: http.Header{"X-Api-Key": {secret}}, want: "X-Api-Key(" + secret + ")"},
		{name: "api-key alias", header: http.Header{"Api-Key": {secret}}, want: "X-Api-Key(" + secret + ")"},
		{name: "x-goog-api-key", header: http.Header{"X-Goog-Api-Key": {secret}}, want: "X-Goog-Api-Key(" + secret + ")"},
		{name: "authorization wins", header: http.Header{"Authorization": {"Bearer " + secret}, "X-Api-Key": {"sk-other"}}, want: "Bearer(" + secret + ")"},
		{name: "non-bearer authorization", header: http.Header{"Authorization": {"Basic abc"}, "X-Api-Key": {secret}}, want: ""},
		{name: "bearer without token", header: http.Header{"Authorization": {"Bearer"}}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ExtractDownstreamAPIKey(tt.header); got != tt.want {
				t.Fatalf("ExtractDownstreamAPIKey() = %q, want %q", got, tt.want)
			}
		})
	}
}
