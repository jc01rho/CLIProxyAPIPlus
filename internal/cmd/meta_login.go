package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

// metaProviderName is the config identifier for the Meta Model API provider.
const metaProviderName = meta.ProviderName

// metaModelName is the default Muse Spark model registered with the meta provider.
const metaModelName = meta.MetaModelName

// DoMetaLogin runs the Meta Model API subscription login: a device-code OAuth
// flow against auth.meta.com, followed by minting a Model API key from the
// subscription. The minted key is stored in the config's openai-compatibility
// section so the standard OpenAI-compatible executor routes requests to
// https://api.meta.ai/v1. Subscriptions do not apply to manually entered API
// keys, so this login is the way to use a Muse subscription through the proxy.
//
// configFilePath may be empty (for example when the config is managed by an
// external store); in that case the minted key is printed for manual placement.
func DoMetaLogin(cfg *config.Config, configFilePath string, options *LoginOptions) {
	if cfg == nil {
		cfg = &config.Config{}
	}

	authSvc := meta.NewMetaAuth(cfg)

	fmt.Println("Starting Meta Model API (Muse Spark) authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(context.Background())
	if err != nil {
		fmt.Printf("Meta authentication failed: %v\n", err)
		return
	}

	verificationURL := strings.TrimSpace(deviceCode.VerificationURIComplete)
	if verificationURL == "" {
		verificationURL = strings.TrimSpace(deviceCode.VerificationURI)
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	fmt.Printf("Then enter this code: %s\n\n", deviceCode.UserCode)

	noBrowser := options != nil && options.NoBrowser
	if !noBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		} else {
			log.Warn("No browser available; please open the URL manually")
		}
	}

	fmt.Println("Waiting for authorization...")
	if deviceCode.ExpiresIn > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", deviceCode.ExpiresIn)
	}

	accessToken, err := authSvc.WaitForAuthorization(context.Background(), deviceCode)
	if err != nil {
		fmt.Printf("Meta authentication failed: %v\n", err)
		return
	}

	fmt.Println("Signed in. Minting Model API key from the subscription...")
	apiKey, err := authSvc.MintAPIKey(context.Background(), accessToken)
	if err != nil {
		fmt.Printf("Meta key mint failed: %v\n", err)
		return
	}

	if strings.TrimSpace(configFilePath) == "" {
		fmt.Println("\nMeta Model API key obtained. Add it to config.yaml under openai-compatibility:")
		printMetaProviderSnippet(apiKey)
		fmt.Println("\nMeta authentication successful")
		return
	}

	if !storeMetaAPIKey(cfg, apiKey) {
		fmt.Println("Meta Model API key is already present in the config; nothing to update.")
		fmt.Println("Meta authentication successful")
		return
	}

	if errSave := config.SaveConfigPreserveComments(configFilePath, cfg); errSave != nil {
		fmt.Printf("Meta authentication succeeded but saving the config failed: %v\n", errSave)
		fmt.Printf("Add this key to the %q openai-compatibility provider manually.\n", metaProviderName)
		printMetaProviderSnippet(apiKey)
		return
	}

	fmt.Printf("Meta Model API key stored in %s under the %q openai-compatibility provider.\n", configFilePath, metaProviderName)
	fmt.Println("Meta authentication successful")
}

// printMetaProviderSnippet prints the openai-compatibility block for manual setup.
func printMetaProviderSnippet(apiKey string) {
	fmt.Printf(`
  openai-compatibility:
    - name: %s
      base-url: %s
      api-key-entries:
        - api-key: %s
      models:
        - name: %s
          alias: %s
          display-name: "Muse Spark 1.3"
          max-context-length: 1048576
          input-modalities: [text, image, pdf, video]
          output-modalities: [text]
`, metaProviderName, meta.APIBaseURL, apiKey, metaModelName, metaModelName)
}

// storeMetaAPIKey delegates to meta.StoreAPIKey; kept as a thin wrapper so the
// login command stays the only place that decides what happens after a mint.
func storeMetaAPIKey(cfg *config.Config, apiKey string) bool {
	return meta.StoreAPIKey(cfg, apiKey)
}
