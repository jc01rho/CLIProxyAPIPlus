package oauthlogin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// browser owns a launched Chromium process and the resources tied to it.
type browser struct {
	cmd        *exec.Cmd
	wsURL      string
	userDir    string
	removeDir  bool
	exited     chan struct{}
	stopOnce   sync.Once
	logf       func(string, ...any)
	stderrTail *ringBuffer
}

type launchConfig struct {
	ExecPath    string
	UserDataDir string
	Headless    bool
	Logf        func(string, ...any)
}

// launchBrowser starts Chromium with remote debugging enabled and returns once
// the DevTools websocket endpoint is known.
func launchBrowser(ctx context.Context, cfg launchConfig) (*browser, error) {
	userDir := cfg.UserDataDir
	removeDir := false
	if userDir == "" {
		dir, err := os.MkdirTemp("", "oauthlogin-profile-")
		if err != nil {
			return nil, fmt.Errorf("oauthlogin: create temp profile: %w", err)
		}
		userDir = dir
		removeDir = true
	} else if err := os.MkdirAll(userDir, 0o700); err != nil {
		return nil, fmt.Errorf("oauthlogin: create profile dir: %w", err)
	}

	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + userDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--disable-dev-shm-usage",
		"--password-store=basic",
		"--use-mock-keychain",
		"about:blank",
	}
	if cfg.Headless {
		args = append([]string{"--headless=new", "--disable-gpu"}, args...)
	}
	if os.Geteuid() == 0 {
		args = append([]string{"--no-sandbox"}, args...)
	}

	cmd := exec.Command(cfg.ExecPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cleanupDir(removeDir, userDir)
		return nil, fmt.Errorf("oauthlogin: stderr pipe: %w", err)
	}
	cmd.Stdout = nil

	if err = cmd.Start(); err != nil {
		cleanupDir(removeDir, userDir)
		return nil, fmt.Errorf("oauthlogin: start chromium: %w", err)
	}

	b := &browser{
		cmd:        cmd,
		userDir:    userDir,
		removeDir:  removeDir,
		exited:     make(chan struct{}),
		logf:       cfg.Logf,
		stderrTail: newRingBuffer(32),
	}
	b.log("chromium started (pid=%d, profile=%s)", cmd.Process.Pid, userDir)

	wsCh := make(chan string, 2)
	go b.scanStderr(stderr, wsCh)
	go b.watchDevToolsPortFile(userDir, wsCh)
	go func() {
		_ = cmd.Wait()
		close(b.exited)
	}()

	select {
	case ws := <-wsCh:
		b.wsURL = ws
		return b, nil
	case <-b.exited:
		b.Stop()
		return nil, fmt.Errorf("oauthlogin: chromium exited before devtools was ready: %s", b.stderrTail.String())
	case <-ctx.Done():
		b.Stop()
		return nil, fmt.Errorf("oauthlogin: waiting for devtools endpoint: %w", ctx.Err())
	}
}

func (b *browser) scanStderr(r io.Reader, wsCh chan<- string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sent := false
	for scanner.Scan() {
		line := scanner.Text()
		b.stderrTail.Add(line)
		if sent {
			continue
		}
		if idx := strings.Index(line, "ws://"); idx >= 0 && strings.Contains(line, "DevTools listening") {
			sent = true
			select {
			case wsCh <- strings.TrimSpace(line[idx:]):
			default:
			}
		}
	}
}

// watchDevToolsPortFile is the fallback discovery path: Chromium writes the
// chosen port and the browser websocket path into the profile directory.
func (b *browser) watchDevToolsPortFile(userDir string, wsCh chan<- string) {
	path := filepath.Join(userDir, "DevToolsActivePort")
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-b.exited:
			return
		case <-ticker.C:
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) < 2 || strings.TrimSpace(lines[0]) == "" {
				continue
			}
			url := fmt.Sprintf("ws://127.0.0.1:%s%s", strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]))
			select {
			case wsCh <- url:
			default:
			}
			return
		}
	}
}

// Stop kills the whole browser process group and removes the temporary
// profile directory. It is safe to call multiple times.
func (b *browser) Stop() {
	b.stopOnce.Do(func() {
		if b.cmd != nil && b.cmd.Process != nil {
			pid := b.cmd.Process.Pid
			if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				_ = b.cmd.Process.Kill()
			}
			select {
			case <-b.exited:
			case <-time.After(10 * time.Second):
				b.log("chromium (pid=%d) did not exit after SIGKILL", pid)
			}
		}
		cleanupDir(b.removeDir, b.userDir)
	})
}

func (b *browser) log(format string, args ...any) {
	if b.logf == nil {
		return
	}
	b.logf("%s", redactText(fmt.Sprintf(format, args...)))
}

func cleanupDir(remove bool, dir string) {
	if remove && dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// ringBuffer keeps the last N stderr lines for diagnostics.
type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newRingBuffer(max int) *ringBuffer {
	return &ringBuffer{max: max}
}

func (r *ringBuffer) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return redactText(strings.Join(r.lines, " | "))
}
