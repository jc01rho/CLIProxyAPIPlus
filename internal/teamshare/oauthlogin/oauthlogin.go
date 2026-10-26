package oauthlogin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Action names accepted by Step.Action.
const (
	ActionGoto         = "goto"
	ActionFill         = "fill"
	ActionClick        = "click"
	ActionPress        = "press"
	ActionWaitSelector = "wait-selector"
	ActionWaitURL      = "wait-url"
)

const (
	defaultRunTimeout  = 2 * time.Minute
	defaultStepTimeout = 30 * time.Second
)

// VirtualCredential is a pre-issued passkey installed into the WebAuthn
// virtual authenticator before the login flow starts.
type VirtualCredential struct {
	// CredentialID is the raw credential id, base64url encoded.
	CredentialID string
	// PrivateKeyPKCS8 is the PKCS#8 private key, standard base64 encoded.
	PrivateKeyPKCS8 string
	// RPID is the relying party id the credential is scoped to.
	RPID string
	// UserHandle is the user handle, base64url encoded. May be empty.
	UserHandle string
	// SignCount is the current signature counter of the credential.
	SignCount uint32
}

// Step is one scripted interaction with the login page. Steps are data: the
// caller owns every selector, so no site specific knowledge lives here.
type Step struct {
	// Action is one of the Action* constants.
	Action string
	// Selector is a CSS selector, used by fill/click/press/wait-selector.
	Selector string
	// Value is the text to type (fill), the key to press (press) or the URL
	// to navigate to (goto).
	Value string
	// URLContains is the substring awaited by wait-url.
	URLContains string
	// Timeout bounds this step; zero means 30s.
	Timeout time.Duration
}

// Options configures Run.
type Options struct {
	// ChromiumPath is the browser binary; empty means FindChromium.
	ChromiumPath string
	// UserDataDir is the Chromium profile directory. Empty means a temporary
	// directory that is removed when Run returns.
	UserDataDir string
	// Headless runs the browser without a visible window.
	Headless bool
	// Credential is the passkey installed into the virtual authenticator.
	// When nil no authenticator is created.
	Credential *VirtualCredential
	// Steps are executed in order after the authorization URL is loaded.
	Steps []Step
	// CallbackURLPrefix marks a successful login: the first navigation whose
	// URL starts with this prefix ends the run.
	CallbackURLPrefix string
	// Timeout bounds the whole run; zero means 2 minutes.
	Timeout time.Duration
	// Logf receives redacted progress messages.
	Logf func(string, ...any)
}

// Result is the outcome of a successful run.
type Result struct {
	// FinalURL is the full callback URL including the OAuth query string.
	FinalURL string
	// SignCount is the credential signature counter read back from the
	// virtual authenticator, so the caller can persist it.
	SignCount uint32
}

// Run drives a headless Chromium through an OAuth login and returns the
// callback URL. The browser process group is always killed and a temporary
// profile is always removed before Run returns.
func Run(ctx context.Context, authURL string, opts Options) (Result, error) {
	var result Result
	if strings.TrimSpace(authURL) == "" {
		return result, errors.New("oauthlogin: authURL is required")
	}
	if strings.TrimSpace(opts.CallbackURLPrefix) == "" {
		return result, errors.New("oauthlogin: CallbackURLPrefix is required")
	}

	execPath := opts.ChromiumPath
	if execPath == "" {
		found, err := FindChromium()
		if err != nil {
			return result, err
		}
		execPath = found
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultRunTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logf := opts.Logf
	logLine := func(format string, args ...any) {
		if logf == nil {
			return
		}
		logf("%s", redactText(fmt.Sprintf(format, args...)))
	}

	b, err := launchBrowser(runCtx, launchConfig{
		ExecPath:    execPath,
		UserDataDir: opts.UserDataDir,
		Headless:    opts.Headless,
		Logf:        logf,
	})
	if err != nil {
		return result, err
	}
	defer b.Stop()

	conn, err := dialCDP(runCtx, b.wsURL)
	if err != nil {
		return result, err
	}
	defer conn.Close()

	// Watch the browser process: if it dies the run must fail fast.
	browserGone := b.exited

	sess, err := attachPage(runCtx, conn)
	if err != nil {
		return result, err
	}

	authenticatorID := ""
	if opts.Credential != nil {
		authenticatorID, err = setupAuthenticator(runCtx, sess, opts.Credential)
		if err != nil {
			return result, err
		}
		logLine("virtual authenticator ready (rpId=%s)", opts.Credential.RPID)
	}

	watcher := newNavWatcher(conn, sess.id, opts.CallbackURLPrefix)
	defer watcher.stop()

	if err = sess.call(runCtx, "Page.navigate", map[string]any{"url": authURL}, nil); err != nil {
		return result, err
	}
	logLine("navigated to %s", RedactURL(authURL))

	stepsErr := make(chan error, 1)
	go func() {
		stepsErr <- runSteps(runCtx, sess, watcher, opts.Steps, logLine)
	}()

	select {
	case final := <-watcher.callback:
		result.FinalURL = final
	case err = <-stepsErr:
		if err != nil {
			// A callback detected while the step was failing still wins.
			select {
			case final := <-watcher.callback:
				result.FinalURL = final
			default:
				return result, err
			}
		} else {
			select {
			case final := <-watcher.callback:
				result.FinalURL = final
			case <-runCtx.Done():
				return result, fmt.Errorf("oauthlogin: steps finished but callback %s never reached: %w", RedactURL(opts.CallbackURLPrefix), runCtx.Err())
			case <-browserGone:
				return result, errors.New("oauthlogin: chromium exited before the callback was reached")
			}
		}
	case <-browserGone:
		return result, errors.New("oauthlogin: chromium exited before the callback was reached")
	case <-runCtx.Done():
		return result, fmt.Errorf("oauthlogin: login did not reach %s: %w", RedactURL(opts.CallbackURLPrefix), runCtx.Err())
	}

	logLine("reached callback %s", RedactURL(result.FinalURL))

	if authenticatorID != "" {
		count, errCount := readSignCount(runCtx, sess, authenticatorID, opts.Credential.CredentialID)
		if errCount != nil {
			return result, errCount
		}
		result.SignCount = count
	}
	return result, nil
}

// pageSession is a flat CDP session attached to a page target.
type pageSession struct {
	conn *cdpConn
	id   string
}

func (s *pageSession) call(ctx context.Context, method string, params any, out any) error {
	return s.conn.call(ctx, s.id, method, params, out)
}

func attachPage(ctx context.Context, conn *cdpConn) (*pageSession, error) {
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := conn.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
		return nil, err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}, &attached); err != nil {
		return nil, err
	}
	sess := &pageSession{conn: conn, id: attached.SessionID}
	for _, method := range []string{"Page.enable", "Runtime.enable", "Network.enable"} {
		if err := sess.call(ctx, method, nil, nil); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

func setupAuthenticator(ctx context.Context, sess *pageSession, cred *VirtualCredential) (string, error) {
	credentialID, err := toStdBase64(cred.CredentialID)
	if err != nil {
		return "", fmt.Errorf("oauthlogin: credential id is not valid base64url")
	}
	if strings.TrimSpace(cred.PrivateKeyPKCS8) == "" {
		return "", errors.New("oauthlogin: credential private key is empty")
	}
	privateKey, err := toStdBase64(cred.PrivateKeyPKCS8)
	if err != nil {
		return "", errors.New("oauthlogin: credential private key is not valid base64")
	}
	if strings.TrimSpace(cred.RPID) == "" {
		return "", errors.New("oauthlogin: credential RPID is empty")
	}
	userHandle := ""
	if cred.UserHandle != "" {
		userHandle, err = toStdBase64(cred.UserHandle)
		if err != nil {
			return "", errors.New("oauthlogin: credential user handle is not valid base64url")
		}
	}

	if err = sess.call(ctx, "WebAuthn.enable", map[string]any{"enableUI": false}, nil); err != nil {
		return "", err
	}
	var added struct {
		AuthenticatorID string `json:"authenticatorId"`
	}
	addParams := map[string]any{
		"options": map[string]any{
			"protocol":                    "ctap2",
			"transport":                   "internal",
			"hasResidentKey":              true,
			"hasUserVerification":         true,
			"isUserVerified":              true,
			"automaticPresenceSimulation": true,
		},
	}
	if err = sess.call(ctx, "WebAuthn.addVirtualAuthenticator", addParams, &added); err != nil {
		return "", err
	}

	credential := map[string]any{
		"credentialId":         credentialID,
		"isResidentCredential": true,
		"rpId":                 cred.RPID,
		"privateKey":           privateKey,
		"signCount":            cred.SignCount,
	}
	if userHandle != "" {
		credential["userHandle"] = userHandle
	}
	if err = sess.call(ctx, "WebAuthn.addCredential", map[string]any{
		"authenticatorId": added.AuthenticatorID,
		"credential":      credential,
	}, nil); err != nil {
		// Never echo key material in the error.
		return "", errors.New("oauthlogin: WebAuthn.addCredential failed: " + sanitizeCDPError(err, credentialID, privateKey, userHandle))
	}
	return added.AuthenticatorID, nil
}

func readSignCount(ctx context.Context, sess *pageSession, authenticatorID, credentialIDB64URL string) (uint32, error) {
	wanted, err := toStdBase64(credentialIDB64URL)
	if err != nil {
		return 0, errors.New("oauthlogin: credential id is not valid base64url")
	}
	var out struct {
		Credentials []struct {
			CredentialID string `json:"credentialId"`
			SignCount    uint32 `json:"signCount"`
		} `json:"credentials"`
	}
	if err = sess.call(ctx, "WebAuthn.getCredentials", map[string]any{"authenticatorId": authenticatorID}, &out); err != nil {
		return 0, errors.New("oauthlogin: WebAuthn.getCredentials failed")
	}
	for _, c := range out.Credentials {
		if equalBase64(c.CredentialID, wanted) {
			return c.SignCount, nil
		}
	}
	return 0, errors.New("oauthlogin: credential disappeared from the virtual authenticator")
}

// navWatcher turns CDP navigation events into callback detection and URL
// notifications for wait-url steps.
type navWatcher struct {
	callback chan string
	cancel   func()

	mu      sync.Mutex
	subs    map[int]chan string
	nextSub int
	seen    []string
	stopped bool
}

func newNavWatcher(conn *cdpConn, sessionID, prefix string) *navWatcher {
	events, cancel := conn.subscribe()
	w := &navWatcher{
		callback: make(chan string, 1),
		cancel:   cancel,
		subs:     make(map[int]chan string),
	}
	go func() {
		for ev := range events {
			if ev.SessionID != sessionID && ev.SessionID != "" {
				continue
			}
			for _, u := range urlsFromEvent(ev) {
				w.publish(u)
				if strings.HasPrefix(u, prefix) {
					select {
					case w.callback <- u:
					default:
					}
				}
			}
		}
	}()
	return w
}

func (w *navWatcher) publish(u string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.seen = append(w.seen, u)
	if len(w.seen) > 256 {
		w.seen = w.seen[len(w.seen)-256:]
	}
	for _, ch := range w.subs {
		select {
		case ch <- u:
		default:
		}
	}
}

// waitURL blocks until a navigation URL containing substr is observed. URLs
// seen before the call are considered too.
func (w *navWatcher) waitURL(ctx context.Context, substr string) (string, error) {
	ch := make(chan string, 64)
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return "", errors.New("oauthlogin: navigation watcher stopped")
	}
	for _, u := range w.seen {
		if strings.Contains(u, substr) {
			w.mu.Unlock()
			return u, nil
		}
	}
	id := w.nextSub
	w.nextSub++
	w.subs[id] = ch
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.subs, id)
		w.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("oauthlogin: wait-url: %w", ctx.Err())
		case u, ok := <-ch:
			if !ok {
				return "", errors.New("oauthlogin: navigation watcher stopped")
			}
			if strings.Contains(u, substr) {
				return u, nil
			}
		}
	}
}

func (w *navWatcher) stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	subs := w.subs
	w.subs = make(map[int]chan string)
	w.mu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
	w.cancel()
}

// urlsFromEvent extracts every URL a CDP event can carry about navigation.
func urlsFromEvent(ev cdpEvent) []string {
	var urls []string
	add := func(u string) {
		if u != "" && u != "about:blank" {
			urls = append(urls, u)
		}
	}
	switch ev.Method {
	case "Page.frameNavigated":
		var p struct {
			Frame struct {
				URL      string `json:"url"`
				ParentID string `json:"parentId"`
			} `json:"frame"`
		}
		if json.Unmarshal(ev.Params, &p) == nil && p.Frame.ParentID == "" {
			add(p.Frame.URL)
		}
	case "Page.navigatedWithinDocument", "Page.frameRequestedNavigation":
		var p struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			add(p.URL)
		}
	case "Network.requestWillBeSent":
		// The callback host often refuses the connection, so the request is
		// the only trace of the final URL.
		var p struct {
			Type    string `json:"type"`
			Request struct {
				URL string `json:"url"`
			} `json:"request"`
			DocumentURL string `json:"documentURL"`
		}
		if json.Unmarshal(ev.Params, &p) == nil && p.Type == "Document" {
			add(p.Request.URL)
		}
	case "Network.responseReceived":
		var p struct {
			Type     string `json:"type"`
			Response struct {
				URL string `json:"url"`
			} `json:"response"`
		}
		if json.Unmarshal(ev.Params, &p) == nil && p.Type == "Document" {
			add(p.Response.URL)
		}
	}
	return urls
}

func runSteps(ctx context.Context, sess *pageSession, watcher *navWatcher, steps []Step, logLine func(string, ...any)) error {
	for i, step := range steps {
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout(step))
		err := runStep(stepCtx, sess, watcher, step)
		cancel()
		if err != nil {
			return fmt.Errorf("oauthlogin: step %d (%s): %w", i+1, step.Action, err)
		}
		logLine("step %d (%s) done", i+1, step.Action)
	}
	return nil
}

func stepTimeout(step Step) time.Duration {
	if step.Timeout > 0 {
		return step.Timeout
	}
	return defaultStepTimeout
}

func runStep(ctx context.Context, sess *pageSession, watcher *navWatcher, step Step) error {
	switch step.Action {
	case ActionGoto:
		if step.Value == "" {
			return errors.New("goto requires Value")
		}
		return sess.call(ctx, "Page.navigate", map[string]any{"url": step.Value}, nil)
	case ActionWaitURL:
		if step.URLContains == "" {
			return errors.New("wait-url requires URLContains")
		}
		_, err := watcher.waitURL(ctx, step.URLContains)
		return err
	case ActionWaitSelector:
		if step.Selector == "" {
			return errors.New("wait-selector requires Selector")
		}
		_, err := evaluate(ctx, sess, waitSelectorJS(step.Selector), true)
		return err
	case ActionFill:
		if step.Selector == "" {
			return errors.New("fill requires Selector")
		}
		if _, err := evaluate(ctx, sess, waitSelectorJS(step.Selector), true); err != nil {
			return err
		}
		_, err := evaluate(ctx, sess, fillJS(step.Selector, step.Value), false)
		return err
	case ActionClick:
		if step.Selector == "" {
			return errors.New("click requires Selector")
		}
		if _, err := evaluate(ctx, sess, waitSelectorJS(step.Selector), true); err != nil {
			return err
		}
		_, err := evaluate(ctx, sess, clickJS(step.Selector), false)
		return err
	case ActionPress:
		return pressKey(ctx, sess, step)
	default:
		return fmt.Errorf("unknown action %q", step.Action)
	}
}

func pressKey(ctx context.Context, sess *pageSession, step Step) error {
	if step.Value == "" {
		return errors.New("press requires Value")
	}
	if step.Selector != "" {
		if _, err := evaluate(ctx, sess, waitSelectorJS(step.Selector), true); err != nil {
			return err
		}
		if _, err := evaluate(ctx, sess, focusJS(step.Selector), false); err != nil {
			return err
		}
	}
	key := step.Value
	text := ""
	code := key
	var vk int
	switch key {
	case "Enter":
		text, vk, code = "\r", 13, "Enter"
	case "Tab":
		text, vk, code = "\t", 9, "Tab"
	case "Escape":
		vk, code = 27, "Escape"
	default:
		if len([]rune(key)) != 1 {
			return fmt.Errorf("unsupported key %q", key)
		}
		text = key
		vk = int([]rune(key)[0])
		code = "Key" + strings.ToUpper(key)
	}
	down := map[string]any{
		"type":                  "keyDown",
		"key":                   key,
		"code":                  code,
		"windowsVirtualKeyCode": vk,
		"nativeVirtualKeyCode":  vk,
	}
	if text != "" {
		down["text"] = text
		down["unmodifiedText"] = text
	}
	if err := sess.call(ctx, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return sess.call(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type":                  "keyUp",
		"key":                   key,
		"code":                  code,
		"windowsVirtualKeyCode": vk,
		"nativeVirtualKeyCode":  vk,
	}, nil)
}

// evaluate runs an expression in the page and returns its JSON value.
func evaluate(ctx context.Context, sess *pageSession, expression string, awaitPromise bool) (json.RawMessage, error) {
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		err := sess.call(ctx, "Runtime.evaluate", map[string]any{
			"expression":    expression,
			"awaitPromise":  awaitPromise,
			"returnByValue": true,
			"userGesture":   true,
		}, &out)
		if err != nil {
			lastErr = err
			if isContextDestroyed(err.Error()) && ctx.Err() == nil {
				continue
			}
			return nil, err
		}
		if out.ExceptionDetails != nil {
			text := out.ExceptionDetails.Text
			if out.ExceptionDetails.Exception != nil && out.ExceptionDetails.Exception.Description != "" {
				text = out.ExceptionDetails.Exception.Description
			}
			lastErr = errors.New(redactText(firstLine(text)))
			if isContextDestroyed(text) && ctx.Err() == nil {
				continue
			}
			return nil, lastErr
		}
		return out.Result.Value, nil
	}
	return nil, lastErr
}

func isContextDestroyed(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "execution context was destroyed") ||
		strings.Contains(lower, "cannot find context") ||
		strings.Contains(lower, "execution context is not available")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func waitSelectorJS(selector string) string {
	return fmt.Sprintf(`new Promise((resolve, reject) => {
  const sel = %s;
  const found = () => document.querySelector(sel);
  if (found()) { resolve(true); return; }
  const root = document.documentElement || document;
  const obs = new MutationObserver(() => { if (found()) { obs.disconnect(); resolve(true); } });
  obs.observe(root, {childList: true, subtree: true, attributes: true});
})`, jsString(selector))
}

func fillJS(selector, value string) string {
	return fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) throw new Error('element not found');
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, 'value');
  el.focus();
  if (setter && setter.set) { setter.set.call(el, %s); } else { el.value = %s; }
  el.dispatchEvent(new Event('input', {bubbles: true}));
  el.dispatchEvent(new Event('change', {bubbles: true}));
  return true;
})()`, jsString(selector), jsString(value), jsString(value))
}

func clickJS(selector string) string {
	return fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) throw new Error('element not found');
  el.click();
  return true;
})()`, jsString(selector))
}

func focusJS(selector string) string {
	return fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) throw new Error('element not found');
  el.focus();
  return true;
})()`, jsString(selector))
}

func jsString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// toStdBase64 accepts base64 or base64url (padded or not) and re-encodes the
// payload as the standard padded base64 the DevTools protocol expects.
func toStdBase64(value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", errors.New("empty value")
	}
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding,
	} {
		if raw, err := enc.DecodeString(v); err == nil {
			return base64.StdEncoding.EncodeToString(raw), nil
		}
	}
	return "", errors.New("not base64")
}

func equalBase64(a, b string) bool {
	an, errA := toStdBase64(a)
	bn, errB := toStdBase64(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return an == bn
}

// sanitizeCDPError strips any secret that may have been echoed back by the
// browser before the message is surfaced.
func sanitizeCDPError(err error, secrets ...string) string {
	msg := redactText(err.Error())
	for _, s := range secrets {
		if s == "" {
			continue
		}
		msg = strings.ReplaceAll(msg, s, "[redacted]")
	}
	return msg
}
