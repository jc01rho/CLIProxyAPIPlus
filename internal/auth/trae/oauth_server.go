// Package trae provides authentication and token management functionality
// for Trae AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Trae API.
package trae

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// OAuthServer handles the local HTTP server for OAuth callbacks.
// It listens for the authorization code response from the OAuth provider
// and captures the necessary parameters to complete the authentication flow.
type OAuthServer struct {
	// server is the underlying HTTP server instance
	server *http.Server
	// port is the port number on which the server listens
	port int
	// resultChan is a channel for sending OAuth results
	resultChan chan *OAuthResult
	// errorChan is a channel for sending OAuth errors
	errorChan chan error
	// mu is a mutex for protecting server state
	mu sync.Mutex
	// running indicates whether the server is currently running
	running bool
}

// OAuthResult contains the result of the OAuth callback.
// It holds either the authorization code and state for successful authentication
// or an error message if the authentication failed.
type OAuthResult struct {
	// Code is the authorization code received from the OAuth provider
	Code string
	// State is the state parameter used to prevent CSRF attacks
	State string
	// Error contains any error message if the OAuth flow failed
	Error string
}

// NewOAuthServer creates a new OAuth callback server.
// It initializes the server with the specified port and creates channels
// for handling OAuth results and errors.
//
// Parameters:
//   - port: The port number on which the server should listen
//
// Returns:
//   - *OAuthServer: A new OAuthServer instance
func NewOAuthServer(port int) *OAuthServer {
	return &OAuthServer{
		port:       port,
		resultChan: make(chan *OAuthResult, 1),
		errorChan:  make(chan error, 1),
	}
}

// Start starts the OAuth callback server.
// It sets up the HTTP handlers for the callback and success endpoints,
// and begins listening on the specified port.
//
// Returns:
//   - error: An error if the server fails to start
func (s *OAuthServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("server is already running")
	}

	if !s.isPortAvailable() {
		return fmt.Errorf("port %d is already in use", s.port)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", s.handleCallback)
	mux.HandleFunc("/success", s.handleSuccess)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	s.running = true

	go func() {
		if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.errorChan <- fmt.Errorf("server failed to start: %w", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	return nil
}

// Stop gracefully stops the OAuth callback server.
// It performs a graceful shutdown of the HTTP server with a timeout.
//
// Parameters:
//   - ctx: The context for controlling the shutdown process
//
// Returns:
//   - error: An error if the server fails to stop gracefully
func (s *OAuthServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.server == nil {
		return nil
	}

	log.Debug("Stopping OAuth callback server")

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := s.server.Shutdown(shutdownCtx)
	s.running = false
	s.server = nil

	return err
}

// WaitForCallback waits for the OAuth callback with a timeout.
// It blocks until either an OAuth result is received, an error occurs,
// or the specified timeout is reached.
//
// Parameters:
//   - timeout: The maximum time to wait for the callback
//
// Returns:
//   - *OAuthResult: The OAuth result if successful
//   - error: An error if the callback times out or an error occurs
func (s *OAuthServer) WaitForCallback(timeout time.Duration) (*OAuthResult, error) {
	select {
	case result := <-s.resultChan:
		return result, nil
	case err := <-s.errorChan:
		return nil, err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for OAuth callback")
	}
}

// handleCallback handles the OAuth callback endpoint.
// It extracts the authorization code and state from the callback URL,
// validates the parameters, and sends the result to the waiting channel.
//
// Parameters:
//   - w: The HTTP response writer
//   - r: The HTTP request
func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	log.Debug("Received OAuth callback")

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	code := query.Get("code")
	state := query.Get("state")
	errorParam := query.Get("error")

	if errorParam != "" {
		log.Errorf("OAuth error received: %s", errorParam)
		result := &OAuthResult{
			Error: errorParam,
		}
		s.sendResult(result)
		http.Error(w, fmt.Sprintf("OAuth error: %s", errorParam), http.StatusBadRequest)
		return
	}

	if code == "" {
		log.Error("No authorization code received")
		result := &OAuthResult{
			Error: "no_code",
		}
		s.sendResult(result)
		http.Error(w, "No authorization code received", http.StatusBadRequest)
		return
	}

	if state == "" {
		log.Error("No state parameter received")
		result := &OAuthResult{
			Error: "no_state",
		}
		s.sendResult(result)
		http.Error(w, "No state parameter received", http.StatusBadRequest)
		return
	}

	result := &OAuthResult{
		Code:  code,
		State: state,
	}
	s.sendResult(result)

	http.Redirect(w, r, "/success", http.StatusFound)
}

// handleSuccess handles the success page endpoint.
// It serves a user-friendly HTML page indicating that authentication was successful.
//
// Parameters:
//   - w: The HTTP response writer
//   - r: The HTTP request
func (s *OAuthServer) handleSuccess(w http.ResponseWriter, r *http.Request) {
	log.Debug("Serving success page")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	query := r.URL.Query()
	setupRequired := query.Get("setup_required") == "true"
	platformURL := query.Get("platform_url")
	if platformURL == "" {
		platformURL = "https://www.trae.ai/"
	}

	if !isValidURL(platformURL) {
		platformURL = "https://www.trae.ai/"
	}

	successHTML := s.generateSuccessHTML(setupRequired, platformURL)

	_, err := w.Write([]byte(successHTML))
	if err != nil {
		log.Errorf("Failed to write success page: %v", err)
	}
}

// isValidURL checks if the URL is a valid http/https URL to prevent XSS
func isValidURL(urlStr string) bool {
	urlStr = strings.TrimSpace(urlStr)
	return strings.HasPrefix(urlStr, "https://") || strings.HasPrefix(urlStr, "http://")
}

// generateSuccessHTML creates the HTML content for the success page.
// It customizes the page based on whether additional setup is required
// and includes a link to the platform.
//
// Parameters:
//   - setupRequired: Whether additional setup is required after authentication
//   - platformURL: The URL to the platform for additional setup
//
// Returns:
//   - string: The HTML content for the success page
func (s *OAuthServer) generateSuccessHTML(setupRequired bool, platformURL string) string {
	html := LoginSuccessHtml

	html = strings.ReplaceAll(html, "{{PLATFORM_URL}}", platformURL)

	if setupRequired {
		setupNotice := strings.ReplaceAll(SetupNoticeHtml, "{{PLATFORM_URL}}", platformURL)
		html = strings.Replace(html, "{{SETUP_NOTICE}}", setupNotice, 1)
	} else {
		html = strings.Replace(html, "{{SETUP_NOTICE}}", "", 1)
	}

	return html
}

// sendResult sends the OAuth result to the waiting channel.
// It ensures that the result is sent without blocking the handler.
//
// Parameters:
//   - result: The OAuth result to send
func (s *OAuthServer) sendResult(result *OAuthResult) {
	select {
	case s.resultChan <- result:
		log.Debug("OAuth result sent to channel")
	default:
		log.Warn("OAuth result channel is full, result dropped")
	}
}

// isPortAvailable checks if the specified port is available.
// It attempts to listen on the port to determine availability.
//
// Returns:
//   - bool: True if the port is available, false otherwise
func (s *OAuthServer) isPortAvailable() bool {
	addr := fmt.Sprintf(":%d", s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	defer func() {
		_ = listener.Close()
	}()
	return true
}

// IsRunning returns whether the server is currently running.
//
// Returns:
//   - bool: True if the server is running, false otherwise
func (s *OAuthServer) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// LoginSuccessHtml is the HTML template displayed to users after successful OAuth authentication.
const LoginSuccessHtml = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Authentication Successful - Trae</title>
    <link rel="icon" type="image/svg+xml" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='%2310b981'%3E%3Cpath d='M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z'/%3E%3C/svg%3E">
    <style>
        * {
            box-sizing: border-box;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            min-height: 100vh;
            margin: 0;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            padding: 1rem;
        }
        .container {
            text-align: center;
            background: white;
            padding: 2.5rem;
            border-radius: 12px;
            box-shadow: 0 10px 25px rgba(0,0,0,0.1);
            max-width: 480px;
            width: 100%;
            animation: slideIn 0.3s ease-out;
        }
        @keyframes slideIn {
            from {
                opacity: 0;
                transform: translateY(-20px);
            }
            to {
                opacity: 1;
                transform: translateY(0);
            }
        }
        .success-icon {
            width: 64px;
            height: 64px;
            margin: 0 auto 1.5rem;
            background: #10b981;
            border-radius: 50%;
            display: flex;
            align-items: center;
            justify-content: center;
            color: white;
            font-size: 2rem;
            font-weight: bold;
        }
        h1 {
            color: #1f2937;
            margin-bottom: 1rem;
            font-size: 1.75rem;
            font-weight: 600;
        }
        .subtitle {
            color: #6b7280;
            margin-bottom: 1.5rem;
            font-size: 1rem;
            line-height: 1.5;
        }
        .setup-notice {
            background: #fef3c7;
            border: 1px solid #f59e0b;
            border-radius: 6px;
            padding: 1rem;
            margin: 1rem 0;
        }
        .setup-notice h3 {
            color: #92400e;
            margin: 0 0 0.5rem 0;
            font-size: 1rem;
        }
        .setup-notice p {
            color: #92400e;
            margin: 0;
            font-size: 0.875rem;
        }
        .setup-notice a {
            color: #1d4ed8;
            text-decoration: none;
        }
        .setup-notice a:hover {
            text-decoration: underline;
        }
        .actions {
            display: flex;
            gap: 1rem;
            justify-content: center;
            flex-wrap: wrap;
            margin-top: 2rem;
        }
        .button {
            padding: 0.75rem 1.5rem;
            border-radius: 8px;
            font-size: 0.875rem;
            font-weight: 500;
            text-decoration: none;
            transition: all 0.2s;
            cursor: pointer;
            border: none;
            display: inline-flex;
            align-items: center;
            gap: 0.5rem;
        }
        .button-primary {
            background: #3b82f6;
            color: white;
        }
        .button-primary:hover {
            background: #2563eb;
            transform: translateY(-1px);
        }
        .button-secondary {
            background: #f3f4f6;
            color: #374151;
            border: 1px solid #d1d5db;
        }
        .button-secondary:hover {
            background: #e5e7eb;
        }
        .countdown {
            color: #9ca3af;
            font-size: 0.75rem;
            margin-top: 1rem;
        }
        .footer {
            margin-top: 2rem;
            padding-top: 1.5rem;
            border-top: 1px solid #e5e7eb;
            color: #9ca3af;
            font-size: 0.75rem;
        }
        .footer a {
            color: #3b82f6;
            text-decoration: none;
        }
        .footer a:hover {
            text-decoration: underline;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="success-icon">✓</div>
        <h1>Authentication Successful!</h1>
        <p class="subtitle">You have successfully authenticated with Trae. You can now close this window and return to your terminal to continue.</p>
        
        {{SETUP_NOTICE}}
        
        <div class="actions">
            <button class="button button-primary" onclick="window.close()">
                <span>Close Window</span>
            </button>
            <a href="{{PLATFORM_URL}}" target="_blank" class="button button-secondary">
                <span>Open Platform</span>
                <span>↗</span>
            </a>
        </div>
        
        <div class="countdown">
            This window will close automatically in <span id="countdown">10</span> seconds
        </div>
        
        <div class="footer">
            <p>Powered by <a href="https://chatgpt.com" target="_blank">ChatGPT</a></p>
        </div>
    </div>
    
    <script>
        let countdown = 10;
        const countdownElement = document.getElementById('countdown');
        
        const timer = setInterval(() => {
            countdown--;
            countdownElement.textContent = countdown;
            
            if (countdown <= 0) {
                clearInterval(timer);
                window.close();
            }
        }, 1000);
        
        // Close window when user presses Escape
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                window.close();
            }
        });
        
        // Focus the close button for keyboard accessibility
        document.querySelector('.button-primary').focus();
    </script>
</body>
</html>`

// SetupNoticeHtml is the HTML template for the setup notice section.
const SetupNoticeHtml = `
        <div class="setup-notice">
            <h3>Additional Setup Required</h3>
            <p>To complete your setup, please visit the <a href="{{PLATFORM_URL}}" target="_blank">Trae</a> to configure your account.</p>
        </div>`
