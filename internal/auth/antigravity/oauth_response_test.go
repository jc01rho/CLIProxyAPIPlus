package antigravity

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestExchangeCodeForTokensDecodesAdvertisedResponseEncodings(t *testing.T) {
	const payload = `{"access_token":"access","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer"}`
	tests := []struct {
		name     string
		encoding string
		encode   func(testing.TB, []byte) []byte
	}{
		{name: "gzip", encoding: "gzip", encode: gzipAntigravityOAuthBody},
		{name: "deflate_zlib", encoding: "deflate", encode: deflateAntigravityOAuthBody},
		{name: "deflate_raw", encoding: "deflate", encode: rawDeflateAntigravityOAuthBody},
		{name: "brotli", encoding: "br", encode: brotliAntigravityOAuthBody},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Encoding": []string{test.encoding}},
					Body:       io.NopCloser(bytes.NewReader(test.encode(t, []byte(payload)))),
					Request:    req,
				}, nil
			})})

			token, errExchange := auth.ExchangeCodeForTokens(context.Background(), "4/0A-test-code", RedirectURI, "", &PKCECodes{CodeVerifier: "verifier"})
			if errExchange != nil {
				t.Fatalf("ExchangeCodeForTokens error: %v", errExchange)
			}
			if token.AccessToken != "access" || token.RefreshToken != "refresh" || token.ExpiresIn != 3600 || token.TokenType != "Bearer" {
				t.Fatalf("unexpected token response: %+v", token)
			}
		})
	}
}

func TestExchangeCodeForTokensDecodesAndMasksCompressedErrorBody(t *testing.T) {
	const payload = `client_secret=secret-client&code_verifier=secret-verifier&error=invalid_grant`
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Encoding": []string{"gzip"}},
			Body:       io.NopCloser(bytes.NewReader(gzipAntigravityOAuthBody(t, []byte(payload)))),
			Request:    req,
		}, nil
	})})

	_, errExchange := auth.ExchangeCodeForTokens(context.Background(), "4/0A-test-code", RedirectURI, "", &PKCECodes{CodeVerifier: "verifier"})
	if errExchange == nil {
		t.Fatal("expected exchange error")
	}
	errText := errExchange.Error()
	if !strings.Contains(errText, "invalid_grant") {
		t.Fatalf("error missing decoded provider diagnostic: %s", errText)
	}
	if strings.Contains(errText, "secret-client") || strings.Contains(errText, "secret-verifier") {
		t.Fatalf("error leaked sensitive value: %s", errText)
	}
	if !strings.Contains(errText, "secr...ient") || !strings.Contains(errText, "secr...fier") {
		t.Fatalf("error did not preserve masked diagnostics: %s", errText)
	}
}

func TestReadAntigravityOAuthResponseBodyDecodesStackedHeaders(t *testing.T) {
	payload := []byte(`{"access_token":"access"}`)
	encoded := brotliAntigravityOAuthBody(t, gzipAntigravityOAuthBody(t, payload))
	tests := []struct {
		name   string
		header http.Header
	}{
		{
			name: "repeated",
			header: func() http.Header {
				header := make(http.Header)
				header.Add("Content-Encoding", "gzip")
				header.Add("Content-Encoding", "br")
				return header
			}(),
		},
		{name: "comma_separated", header: http.Header{"Content-Encoding": []string{"gzip, br"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp := &http.Response{
				Header: test.header,
				Body:   io.NopCloser(bytes.NewReader(encoded)),
			}
			got, errRead := readAntigravityOAuthResponseBody(resp)
			if errRead != nil {
				t.Fatalf("readAntigravityOAuthResponseBody error: %v", errRead)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("decoded body = %q, want %q", got, payload)
			}
		})
	}
}

func gzipAntigravityOAuthBody(tb testing.TB, input []byte) []byte {
	tb.Helper()
	var output bytes.Buffer
	writer := gzip.NewWriter(&output)
	if _, errWrite := writer.Write(input); errWrite != nil {
		tb.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		tb.Fatal(errClose)
	}
	return output.Bytes()
}

func deflateAntigravityOAuthBody(tb testing.TB, input []byte) []byte {
	tb.Helper()
	var output bytes.Buffer
	writer := zlib.NewWriter(&output)
	if _, errWrite := writer.Write(input); errWrite != nil {
		tb.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		tb.Fatal(errClose)
	}
	return output.Bytes()
}

func rawDeflateAntigravityOAuthBody(tb testing.TB, input []byte) []byte {
	tb.Helper()
	var output bytes.Buffer
	writer, errWriter := flate.NewWriter(&output, flate.DefaultCompression)
	if errWriter != nil {
		tb.Fatal(errWriter)
	}
	if _, errWrite := writer.Write(input); errWrite != nil {
		tb.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		tb.Fatal(errClose)
	}
	return output.Bytes()
}

func brotliAntigravityOAuthBody(tb testing.TB, input []byte) []byte {
	tb.Helper()
	var output bytes.Buffer
	writer := brotli.NewWriter(&output)
	if _, errWrite := writer.Write(input); errWrite != nil {
		tb.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		tb.Fatal(errClose)
	}
	return output.Bytes()
}
