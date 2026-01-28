// Package trae provides native OAuth URL generation for Trae.
package trae

import (
	"fmt"
	"net/url"

	"github.com/google/uuid"
)

const (
	nativeAuthBaseURL = "https://www.trae.ai/authorization"
)

// GenerateNativeAuthURL generates the Trae native OAuth authorization URL.
// It returns the full authorization URL and the generated login trace ID.
func GenerateNativeAuthURL(callbackURL string, appVersion string) (authURL string, loginTraceID string, err error) {
	machineID, err := GenerateMachineID()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate machine id: %w", err)
	}

	deviceID, err := GenerateDeviceID(machineID)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate device id: %w", err)
	}

	loginTraceID = uuid.New().String()

	params := url.Values{}
	params.Add("login_version", "1")
	params.Add("auth_from", "trae")
	params.Add("login_channel", "native_ide")
	params.Add("plugin_version", appVersion)
	params.Add("auth_type", "local")
	params.Add("client_id", traeClientID)
	params.Add("redirect", "1")
	params.Add("login_trace_id", loginTraceID)
	params.Add("auth_callback_url", callbackURL)
	params.Add("machine_id", machineID)
	params.Add("device_id", deviceID)
	params.Add("x_device_id", deviceID)
	params.Add("x_machine_id", machineID)
	params.Add("x_device_brand", GetDeviceBrand())
	params.Add("x_device_type", GetDeviceType())
	params.Add("x_os_version", GetOSVersion())
	params.Add("x_env", "")
	params.Add("x_app_version", appVersion)
	params.Add("x_app_type", "stable")

	authURL = fmt.Sprintf("%s?%s", nativeAuthBaseURL, params.Encode())
	return authURL, loginTraceID, nil
}
