package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoQoderLogin runs the Qoder OAuth device flow + PKCE login and saves the
// resulting auth record to the manager's storage directory.
func DoQoderLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
	}
	if pat := options.PersonalToken; pat != "" {
		authOpts.Metadata["personal_token"] = pat
	}

	record, savedPath, err := manager.Login(context.Background(), "qoder", cfg, authOpts)
	if err != nil {
		log.Errorf("Qoder authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Qoder authentication successful!")
}

// DoQoderImport reads a Qoder CLI credential file from one of the
// well-known default paths (~/.qoder/.auth/user or ~/.qoderwork/.auth/user)
// and registers it with the auth manager. This is the path of least
// resistance for users who are already logged in to the Qoder CLI.
func DoQoderImport(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()
	authenticator := sdkAuth.NewQoderAuthenticator()
	record, err := authenticator.(*sdkAuth.QoderAuthenticator).ImportFromCredentialFile()
	if err != nil {
		log.Errorf("Qoder credential import failed: %v", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Install and log in to the Qoder CLI (qoder CLI)")
		fmt.Println("2. Make sure ~/.qoder/.auth/user (or ~/.qoderwork/.auth/user) exists")
		fmt.Println("3. Re-run this command")
		return
	}

	savedPath, err := manager.SaveAuth(record, cfg)
	if err != nil {
		log.Errorf("Failed to save imported auth: %v", err)
		return
	}
	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Imported as %s\n", record.Label)
	}
	fmt.Println("Qoder credential import successful!")
}
