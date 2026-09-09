package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

// DoAlysisLogin handles the Alysis Code device flow using the shared
// authentication manager. It initiates the device login (user approves a code
// on https://alysiscode.com/activate) and saves the gateway key to the
// configured auth directory.
func DoAlysisLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     map[string]string{},
	}

	_, savedPath, err := manager.Login(context.Background(), "alysis", cfg, authOpts)
	if err != nil {
		fmt.Printf("Alysis authentication failed: %v\n", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}

	fmt.Println("Alysis authentication successful!")
}
