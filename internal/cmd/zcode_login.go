package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoZcodeLogin triggers the GLM ZCode OAuth flow (UNOFFICIAL, opt-in).
// The user completes Z.AI login in the browser and pastes the redirect URL or
// authorization code (the CLI cannot catch the zcode:// redirect).
func DoZcodeLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()
	authenticator := sdkAuth.NewZcodeAuthenticator()
	record, err := authenticator.Login(context.Background(), cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	})
	if err != nil {
		log.Errorf("ZCode authentication failed: %v", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Complete the Z.AI login in the browser")
		fmt.Println("2. Paste the full redirect URL or authorization code when prompted")
		fmt.Println("3. This is an UNOFFICIAL ZCode-based login — it may stop working or violate ZCode/Z.AI Terms of Service")
		return
	}

	savedPath, err := manager.SaveAuth(record, cfg)
	if err != nil {
		log.Errorf("Failed to save auth: %v", err)
		return
	}
	fmt.Printf("ZCode auth saved to %s\n", savedPath)
}
