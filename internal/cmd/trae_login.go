package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// DoTraeLogin handles the Trae Native OAuth authentication flow.
// This is the default login method using Native OAuth flow.
func DoTraeLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = func(prompt string) (string, error) {
			fmt.Println()
			fmt.Println(prompt)
			var value string
			_, err := fmt.Scanln(&value)
			return value, err
		}
	}

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     map[string]string{},
		Prompt:       promptFn,
	}

	authenticator := sdkAuth.NewTraeAuthenticator()
	record, err := authenticator.LoginWithNative(context.Background(), cfg, authOpts)
	if err != nil {
		var emailErr *sdkAuth.EmailRequiredError
		if errors.As(err, &emailErr) {
			log.Error(emailErr.Error())
			return
		}
		fmt.Printf("Trae Native OAuth authentication failed: %v\n", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Make sure you complete the login in the browser")
		fmt.Println("2. If callback fails, try: --trae-import (after logging in via Trae IDE)")
		return
	}

	savedPath, err := manager.SaveAuth(record, cfg)
	if err != nil {
		log.Errorf("Failed to save auth: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Trae Native OAuth authentication successful!")
}

// DoTraeImport imports Trae token from Trae IDE's token file.
// This is useful for users who have already logged in via Trae IDE
// and want to use the same credentials in CLI Proxy API.
func DoTraeImport(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	authSvc := trae.NewTraeAuth(cfg)
	bundle, err := authSvc.ImportExistingTraeToken()
	if err != nil {
		log.Errorf("Trae token import failed: %v", err)
		fmt.Println("\nMake sure you have logged in to Trae IDE first:")
		fmt.Println("1. Open Trae IDE")
		fmt.Println("2. Complete the login process")
		fmt.Println("3. Run this command again")
		return
	}

	if bundle == nil {
		fmt.Println("No existing Trae token found.")
		fmt.Println("Please use 'trae-login' to authenticate via Native OAuth.")
		return
	}

	tokenStorage := authSvc.CreateTokenStorage(&bundle.TokenData)
	fileName := fmt.Sprintf("trae-%s.json", tokenStorage.Email)
	metadata := map[string]any{
		"email": tokenStorage.Email,
	}

	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "trae",
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}

	savedPath, err := manager.SaveAuth(record, cfg)
	if err != nil {
		log.Errorf("Failed to save auth: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if tokenStorage.Email != "" {
		fmt.Printf("Imported as %s\n", tokenStorage.Email)
	}
	fmt.Println("Trae token import successful!")
}
