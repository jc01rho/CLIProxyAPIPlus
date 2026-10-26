package oauthlogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// cdpMessage is a single DevTools Protocol frame (request, response or event).
type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *cdpError) Error() string {
	if e.Data != "" {
		return redactText(fmt.Sprintf("cdp error %d: %s (%s)", e.Code, e.Message, e.Data))
	}
	return redactText(fmt.Sprintf("cdp error %d: %s", e.Code, e.Message))
}

// cdpEvent is an event delivered to subscribers.
type cdpEvent struct {
	Method    string
	SessionID string
	Params    json.RawMessage
}

type pendingCall struct {
	ch chan cdpMessage
}

// cdpConn is a multiplexing CDP client over a single websocket connection.
type cdpConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]*pendingCall
	subs     map[int]chan cdpEvent
	nextSub  int
	closed   bool
	closeErr error

	done chan struct{}
}

func dialCDP(ctx context.Context, wsURL string) (*cdpConn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 20 * time.Second,
		ReadBufferSize:   64 * 1024,
		WriteBufferSize:  64 * 1024,
	}
	ws, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("oauthlogin: connect devtools: %w", err)
	}
	ws.SetReadLimit(64 * 1024 * 1024)
	c := &cdpConn{
		ws:      ws,
		pending: make(map[int64]*pendingCall),
		subs:    make(map[int]chan cdpEvent),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *cdpConn) readLoop() {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.shutdown(err)
			return
		}
		var msg cdpMessage
		if errUnmarshal := json.Unmarshal(data, &msg); errUnmarshal != nil {
			continue
		}
		if msg.ID != 0 {
			c.mu.Lock()
			call := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if call != nil {
				call.ch <- msg
			}
			continue
		}
		if msg.Method == "" {
			continue
		}
		ev := cdpEvent{Method: msg.Method, SessionID: msg.SessionID, Params: msg.Params}
		c.mu.Lock()
		for _, ch := range c.subs {
			select {
			case ch <- ev:
			default: // subscriber is too slow; drop the event rather than stall the reader
			}
		}
		c.mu.Unlock()
	}
}

func (c *cdpConn) shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = err
	pending := c.pending
	c.pending = make(map[int64]*pendingCall)
	subs := c.subs
	c.subs = make(map[int]chan cdpEvent)
	c.mu.Unlock()

	for _, call := range pending {
		call.ch <- cdpMessage{Error: &cdpError{Code: -1, Message: "connection closed: " + err.Error()}}
	}
	for _, ch := range subs {
		close(ch)
	}
	close(c.done)
	_ = c.ws.Close()
}

// Close tears the websocket down.
func (c *cdpConn) Close() {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	_ = c.ws.Close()
	<-c.done
}

// subscribe returns a channel receiving every event until the returned
// cancel function is called or the connection dies.
func (c *cdpConn) subscribe() (<-chan cdpEvent, func()) {
	ch := make(chan cdpEvent, 512)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	id := c.nextSub
	c.nextSub++
	c.subs[id] = ch
	c.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			c.mu.Lock()
			if sub, ok := c.subs[id]; ok {
				delete(c.subs, id)
				close(sub)
			}
			c.mu.Unlock()
		})
	}
}

// call issues a CDP command and waits for its reply. sessionID may be empty
// for browser-scoped commands.
func (c *cdpConn) call(ctx context.Context, sessionID, method string, params any, out any) error {
	var rawParams json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("oauthlogin: encode %s params: %w", method, err)
		}
		rawParams = encoded
	}

	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		return fmt.Errorf("oauthlogin: devtools connection closed: %w", err)
	}
	c.nextID++
	id := c.nextID
	call := &pendingCall{ch: make(chan cdpMessage, 1)}
	c.pending[id] = call
	c.mu.Unlock()

	payload, err := json.Marshal(cdpMessage{ID: id, Method: method, Params: rawParams, SessionID: sessionID})
	if err != nil {
		return fmt.Errorf("oauthlogin: encode %s: %w", method, err)
	}
	c.writeMu.Lock()
	err = c.ws.WriteMessage(websocket.TextMessage, payload)
	c.writeMu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("oauthlogin: send %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("oauthlogin: %s: %w", method, ctx.Err())
	case msg := <-call.ch:
		if msg.Error != nil {
			return fmt.Errorf("oauthlogin: %s: %w", method, errors.New(msg.Error.Error()))
		}
		if out != nil && len(msg.Result) > 0 {
			if errUnmarshal := json.Unmarshal(msg.Result, out); errUnmarshal != nil {
				return fmt.Errorf("oauthlogin: decode %s result: %w", method, errUnmarshal)
			}
		}
		return nil
	}
}
