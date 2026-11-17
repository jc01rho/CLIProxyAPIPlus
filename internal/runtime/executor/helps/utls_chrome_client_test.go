package helps

import (
	"net/http"
	"testing"
)

type stubRoundTripper struct{ calls int }

func (s *stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	s.calls++
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func TestChromeSchemeRoundTripperRoutesByScheme(t *testing.T) {
	chrome, fallback := &stubRoundTripper{}, &stubRoundTripper{}
	rt := &chromeSchemeRoundTripper{chrome: chrome, fallback: fallback}
	for _, tc := range []struct {
		url        string
		wantChrome int
		wantPlain  int
	}{
		{"https://example.test/v1/chat/completions", 1, 0},
		{"http://127.0.0.1:8080/v1/chat/completions", 0, 1},
	} {
		chrome.calls, fallback.calls = 0, 0
		req, err := http.NewRequest(http.MethodPost, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = rt.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
		if chrome.calls != tc.wantChrome || fallback.calls != tc.wantPlain {
			t.Fatalf("%s: chrome=%d fallback=%d, want chrome=%d fallback=%d", tc.url, chrome.calls, fallback.calls, tc.wantChrome, tc.wantPlain)
		}
	}
}
