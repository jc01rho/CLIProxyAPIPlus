// Package devin implements Devin PKCE OAuth authentication and token exchange.
package devin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	// DefaultDevinAuthorizeURL is the browser authorization endpoint for Devin CLI.
	DefaultDevinAuthorizeURL = "https://app.devin.ai/auth/cli/continue"

	// DefaultCodeiumAPIServer is the Connect RPC endpoint for SeatManagementService.
	DefaultCodeiumAPIServer = "https://server.codeium.com"

	// ExchangePKCEPath is the Connect RPC path for exchanging authorization codes.
	ExchangePKCEPath = "/exa.seat_management_pb.SeatManagementService/ExchangePKCEAuthorizationCode"

	// ExchangeDevinCLIPKCEPath is the alternative Connect RPC path for Devin CLI codes.
	ExchangeDevinCLIPKCEPath = "/exa.seat_management_pb.SeatManagementService/ExchangeDevinCLIPKCECode"

	devinExchangeTimeout = 30 * time.Second
)

// PKCECodes holds the code verifier and challenge for Devin OAuth.
type PKCECodes struct {
	Verifier  string `json:"verifier"`
	Challenge string `json:"challenge"`
}

// TokenResponse represents the parsed tokens from ExchangePKCEAuthorizationCode.
type TokenResponse struct {
	APIKey          string `json:"api_key"`
	APIServerURL    string `json:"api_server_url"`
	DevinWebappHost string `json:"devin_webapp_host"`
	DevinAPIURL     string `json:"devin_api_url"`
	SessionToken    string `json:"session_token"`
}

// GeneratePKCE creates a cryptographic PKCE verifier (43-128 chars URL-safe) and S256 challenge.
func GeneratePKCE() (*PKCECodes, error) {
	verifierBytes := make([]byte, 64)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, fmt.Errorf("devin: failed to generate PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])

	return &PKCECodes{
		Verifier:  verifier,
		Challenge: challenge,
	}, nil
}

// GenerateAuthURL builds the browser continue URL for Devin CLI login.
func GenerateAuthURL(state string, pkce *PKCECodes) (string, error) {
	if pkce == nil || pkce.Challenge == "" {
		return "", fmt.Errorf("devin: pkce challenge is required")
	}

	params := url.Values{}
	params.Set("state", state)
	params.Set("prompt", "select_account")
	params.Set("code_challenge", pkce.Challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("cli_pkce_marker", "1")

	return fmt.Sprintf("%s?%s", DefaultDevinAuthorizeURL, params.Encode()), nil
}

// EncodeExchangeRequest constructs the raw protobuf bytes for ExchangePKCEAuthorizationCodeRequest:
//
//	field 1 (wire 2): code (string, stripped of "wspkce$" if present)
//	field 2 (wire 2): code_verifier (string)
func EncodeExchangeRequest(code, verifier string) []byte {
	cleanCode := strings.TrimSpace(code)
	cleanCode = strings.TrimPrefix(cleanCode, "wspkce$")

	var buf bytes.Buffer
	writeStringField(&buf, 1, cleanCode)
	writeStringField(&buf, 2, strings.TrimSpace(verifier))
	return buf.Bytes()
}

func writeStringField(buf *bytes.Buffer, fieldNum int, val string) {
	if val == "" {
		return
	}
	tag := (fieldNum << 3) | 2
	encodeVarint(buf, uint64(tag))
	data := []byte(val)
	encodeVarint(buf, uint64(len(data)))
	buf.Write(data)
}

func encodeVarint(buf *bytes.Buffer, v uint64) {
	var tmp [10]byte
	n := binary.PutUvarint(tmp[:], v)
	buf.Write(tmp[:n])
}

// DecodeExchangeResponse unpacks ExchangePKCEAuthorizationCodeResponse from raw protobuf bytes:
//
//	field 1 (wire 2): api_key
//	field 2 (wire 2): api_server_url
//	field 3 (wire 2): devin_webapp_host
//	field 4 (wire 2): devin_api_url
//	field 5 (wire 2): session_token
func DecodeExchangeResponse(data []byte) (*TokenResponse, error) {
	resp := &TokenResponse{}
	idx := 0
	length := len(data)

	for idx < length {
		tagVar, n := binary.Uvarint(data[idx:])
		if n <= 0 {
			break
		}
		idx += n

		fieldNum := tagVar >> 3
		wireType := tagVar & 0x7

		switch wireType {
		case 0: // varint
			_, vn := binary.Uvarint(data[idx:])
			if vn <= 0 {
				return resp, nil
			}
			idx += vn
		case 2: // length-delimited
			flen, ln := binary.Uvarint(data[idx:])
			if ln <= 0 {
				return resp, nil
			}
			idx += ln
			if idx+int(flen) > length {
				return resp, nil
			}
			valBytes := data[idx : idx+int(flen)]
			idx += int(flen)

			strVal := string(valBytes)
			switch fieldNum {
			case 1:
				resp.APIKey = strVal
			case 2:
				resp.APIServerURL = strVal
			case 3:
				resp.DevinWebappHost = strVal
			case 4:
				resp.DevinAPIURL = strVal
			case 5:
				resp.SessionToken = strVal
			}
		default:
			// Unknown wire type; return what we parsed so far.
			return resp, nil
		}
	}

	return resp, nil
}

// ExchangeCode exchanges an authorization code for Devin credentials via Connect RPC.
// It tries both ExchangeDevinCLIPKCEPath and ExchangePKCEPath to support both
// Devin CLI and Windsurf authorization codes.
func ExchangeCode(ctx context.Context, code, verifier, apiServer string) (*TokenResponse, error) {
	server := strings.TrimRight(strings.TrimSpace(apiServer), "/")
	if server == "" {
		server = DefaultCodeiumAPIServer
	}

	endpoints := []string{ExchangeDevinCLIPKCEPath, ExchangePKCEPath}
	if strings.HasPrefix(strings.TrimSpace(code), "wspkce$") {
		endpoints = []string{ExchangePKCEPath, ExchangeDevinCLIPKCEPath}
	}

	var lastErr error
	for _, ep := range endpoints {
		tokens, err := doExchangeRPC(ctx, server+ep, code, verifier)
		if err == nil {
			return tokens, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func doExchangeRPC(ctx context.Context, reqURL, code, verifier string) (*TokenResponse, error) {
	payload := EncodeExchangeRequest(code, verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("devin: failed to create exchange request: %w", err)
	}

	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")

	client := &http.Client{Timeout: devinExchangeTimeout}
	httpResp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("devin: exchange request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("devin: failed to read exchange response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errObj struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &errObj) == nil && errObj.Message != "" {
			return nil, fmt.Errorf("devin: exchange error (%s): %s", errObj.Code, errObj.Message)
		}
		return nil, fmt.Errorf("devin: exchange returned HTTP %d: %s", httpResp.StatusCode, string(body))
	}

	tokens, err := DecodeExchangeResponse(body)
	if err != nil {
		return nil, fmt.Errorf("devin: failed to decode exchange response: %w", err)
	}

	if tokens.APIKey == "" && tokens.SessionToken == "" {
		return nil, fmt.Errorf("devin: exchange succeeded but returned empty credentials")
	}

	if tokens.APIServerURL == "" {
		tokens.APIServerURL = DefaultCodeiumAPIServer
	}
	if tokens.DevinAPIURL == "" {
		tokens.DevinAPIURL = "https://api.devin.ai"
	}
	if tokens.DevinWebappHost == "" {
		tokens.DevinWebappHost = "https://app.devin.ai"
	}

	return tokens, nil
}

// BuildAuthRecord converts TokenResponse into a CLIProxyAPI core Auth record.
func BuildAuthRecord(tokens *TokenResponse, shortID string) *cliproxyauth.Auth {
	if shortID == "" {
		shortID = fmt.Sprintf("%x", time.Now().UnixNano())[:8]
	}
	fileName := fmt.Sprintf("devin-%s.json", shortID)

	primaryKey := strings.TrimSpace(tokens.APIKey)
	if primaryKey == "" {
		primaryKey = strings.TrimSpace(tokens.SessionToken)
	}

	metadata := map[string]any{
		"type":              "devin",
		"api_key":           primaryKey,
		"api_server_url":    tokens.APIServerURL,
		"devin_webapp_host": tokens.DevinWebappHost,
		"devin_api_url":     tokens.DevinAPIURL,
		"session_token":     tokens.SessionToken,
		"timestamp":         time.Now().UnixMilli(),
	}

	return &cliproxyauth.Auth{
		ID:       fileName,
		Provider: "devin",
		FileName: fileName,
		Label:    fmt.Sprintf("devin-%s", shortID),
		Attributes: map[string]string{
			"api_key":           primaryKey,
			"base_url":          tokens.APIServerURL,
			"devin_api_url":     tokens.DevinAPIURL,
			"session_token":     tokens.SessionToken,
			"devin_webapp_host": tokens.DevinWebappHost,
		},
		Metadata: metadata,
	}
}
