// Package trae provides device fingerprinting utilities for Trae native OAuth flow.
package trae

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/denisbrodbeck/machineid"
	log "github.com/sirupsen/logrus"
)

// GenerateMachineID generates a consistent machine identifier using machineid library.
// Returns the same ID for the same machine across sessions.
func GenerateMachineID() (string, error) {
	id, err := machineid.ProtectedID("trae")
	if err != nil {
		log.Debugf("trae: failed to generate machine id: %v", err)
		return "", fmt.Errorf("failed to generate machine id: %w", err)
	}
	return id, nil
}

// GenerateDeviceID generates a unique device identifier combining machine, user, and platform info.
// Format: SHA256(hostname + username + machineID + platform)
func GenerateDeviceID(machineID string) (string, error) {
	if machineID == "" {
		return "", fmt.Errorf("machineID cannot be empty")
	}

	hostname, err := os.Hostname()
	if err != nil {
		log.Debugf("trae: failed to get hostname: %v", err)
		hostname = "unknown"
	}

	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("USERNAME")
	}
	if username == "" {
		username = "unknown"
	}

	platform := runtime.GOOS

	// Combine all identifiers
	combined := fmt.Sprintf("%s:%s:%s:%s", hostname, username, machineID, platform)

	// Generate SHA256 hash
	hash := sha256.Sum256([]byte(combined))
	deviceID := hex.EncodeToString(hash[:])

	return deviceID, nil
}

// GetPlatform returns the current platform name.
func GetPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "mac"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return runtime.GOOS
	}
}

// GetDeviceBrand returns the hardware brand of the device.
func GetDeviceBrand() string {
	switch runtime.GOOS {
	case "darwin":
		return "Apple"
	default:
		return "unknown"
	}
}

// GetDeviceType returns the type of the device (windows, mac, linux).
func GetDeviceType() string {
	switch runtime.GOOS {
	case "darwin":
		return "mac"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return "unknown"
	}
}

// GetOSVersion returns the actual OS version.
func GetOSVersion() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "linux":
		if data, err := os.ReadFile("/etc/os-release"); err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					version := strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
					if version != "" {
						return version
					}
				}
			}
		}
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}
