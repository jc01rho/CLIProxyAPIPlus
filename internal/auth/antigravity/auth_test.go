package antigravity

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBuildAuthURLMatchesReferenceQueryOrder(t *testing.T) {
	auth := NewAntigravityAuth(nil, nil)
	got := auth.BuildAuthURL("state value", "http://localhost:51121/oauth-callback", &PKCECodes{CodeChallenge: "challenge/value"})
	want := AuthEndpoint + "?" + "client_id=" + ClientID + "&response_type=code&redirect_uri=http%3A%2F%2Flocalhost%3A51121%2Foauth-callback&scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcloud-platform+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fuserinfo.email+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fuserinfo.profile+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcclog+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fexperimentsandconfigs&code_challenge=challenge%2Fvalue&code_challenge_method=S256&state=state+value&access_type=offline&prompt=consent"
	if got != want {
		t.Fatalf("auth URL = %q, want %q", got, want)
	}
}

func TestExchangeCodeForTokensMatchesReferenceBodyAndHeaders(t *testing.T) {
	var gotBody string
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		gotBody = string(body)
		if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded;charset=UTF-8" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := req.Header.Get("Accept"); got != "*/*" {
			t.Fatalf("Accept = %q", got)
		}
		if got := req.Header.Get("Accept-Encoding"); got != "gzip, deflate, br" {
			t.Fatalf("Accept-Encoding = %q", got)
		}
		if got := req.Header.Get("User-Agent"); got != GeminicliUserAgent {
			t.Fatalf("User-Agent = %q", got)
		}
		return jsonResponse(`{"access_token":"access","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer"}`), nil
	})})

	if _, err := auth.ExchangeCodeForTokens(context.Background(), "code value", "http://localhost:51121/oauth-callback", "", &PKCECodes{CodeVerifier: "verifier/value"}); err != nil {
		t.Fatalf("ExchangeCodeForTokens error: %v", err)
	}
	want := "client_id=" + ClientID + "&client_secret=" + ClientSecret + "&code=code+value&grant_type=authorization_code&redirect_uri=http%3A%2F%2Flocalhost%3A51121%2Foauth-callback&code_verifier=verifier%2Fvalue"
	if gotBody != want {
		t.Fatalf("body = %q, want %q", gotBody, want)
	}
}

func TestExchangeCodeForTokensMasksSensitiveErrorBody(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`client_secret=secret-client&code_verifier=secret-verifier&error=invalid_grant`)), Header: make(http.Header), Request: req}, nil
	})})

	_, err := auth.ExchangeCodeForTokens(context.Background(), "code", "http://localhost:51121/oauth-callback", "", &PKCECodes{CodeVerifier: "verifier"})
	if err == nil {
		t.Fatal("expected exchange error")
	}
	errText := err.Error()
	if strings.Contains(errText, "secret-client") || strings.Contains(errText, "secret-verifier") {
		t.Fatalf("error leaked sensitive value: %s", errText)
	}
	if !strings.Contains(errText, "secr...ient") || !strings.Contains(errText, "secr...fier") {
		t.Fatalf("error did not preserve masked diagnostics: %s", errText)
	}
}

func TestFetchProjectIDFromLoadCodeAssist(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected request URL: %s", req.URL.String())
		}
		assertLoadCodeAssistHeaders(t, req)
		assertJSONContains(t, req, `"ideType":"ANTIGRAVITY"`)
		return jsonResponse(`{"cloudaicompanionProject":"cogent-snow-4mnnp"}`), nil
	})})

	projectID, err := auth.FetchProjectID(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("FetchProjectID error: %v", err)
	}
	if projectID != "cogent-snow-4mnnp" {
		t.Fatalf("projectID = %q", projectID)
	}
}

func TestFetchProjectIDFallsBackToDailyOnboardUser(t *testing.T) {
	var sawOnboard bool
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			assertLoadCodeAssistHeaders(t, req)
			return jsonResponse(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`), nil
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
			sawOnboard = true
			assertOnboardUserHeaders(t, req)
			assertJSONContains(t, req, `"tier_id":"free-tier"`)
			assertJSONContains(t, req, `"ide_type":"ANTIGRAVITY"`)
			return jsonResponse(`{
				"done": true,
				"response": {
					"cloudaicompanionProject": {
						"id": "cogent-snow-4mnnp",
						"name": "cogent-snow-4mnnp",
						"projectNumber": "22597072101"
					}
				}
			}`), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL.String())
			return nil, nil
		}
	})})

	projectID, err := auth.FetchProjectID(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("FetchProjectID error: %v", err)
	}
	if !sawOnboard {
		t.Fatalf("expected onboardUser fallback")
	}
	if projectID != "cogent-snow-4mnnp" {
		t.Fatalf("projectID = %q", projectID)
	}
}

func assertLoadCodeAssistHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := req.Header.Get("X-Goog-Api-Client"); got != "" {
		t.Fatalf("X-Goog-Api-Client = %q, want empty", got)
	}
	if got := req.Header.Get("User-Agent"); strings.Contains(got, "google-api-nodejs-client/") {
		t.Fatalf("User-Agent = %q", got)
	}
}

func assertOnboardUserHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := req.Header.Get("X-Goog-Api-Client"); got != "gl-node/22.21.1" {
		t.Fatalf("X-Goog-Api-Client = %q", got)
	}
	if got := req.Header.Get("User-Agent"); !strings.Contains(got, "google-api-nodejs-client/10.3.0") {
		t.Fatalf("User-Agent = %q", got)
	}
}

func assertJSONContains(t *testing.T, req *http.Request, want string) {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyText := string(body)
	req.Body = io.NopCloser(strings.NewReader(bodyText))
	if !strings.Contains(bodyText, want) {
		t.Fatalf("body missing %s: %s", want, bodyText)
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
