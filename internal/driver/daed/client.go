package daed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yhyld/dae-tui/internal/driver"
)

// Client is a minimal GraphQL-over-HTTP client for the daed API: one POST
// endpoint, Bearer JWT auth, no websockets, no codegen.
type Client struct {
	endpoint string
	hc       *http.Client

	mu    sync.Mutex
	token string
	// reauthDone is non-nil while a re-auth is in flight and is closed when
	// it finishes. Concurrent "access denied" responses share that one
	// refresh instead of each failing on its own.
	reauthDone chan struct{}
	// onReAuth is invoked when the backend answers "access denied"; it
	// should refresh the token (e.g. re-run token() with stored
	// credentials) or return an error. Installed once at construction.
	onReAuth func(ctx context.Context, c *Client) error
}

func NewClient(endpoint string) *Client {
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		// Generous per-request timeout: probing many nodes can take a while.
		hc: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetToken installs the auth token used for subsequent requests.
func (c *Client) SetToken(t string) {
	c.mu.Lock()
	c.token = t
	c.mu.Unlock()
}

// Token returns the current token.
func (c *Client) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// SetReAuth installs the re-authentication hook (see onReAuth).
func (c *Client) SetReAuth(f func(ctx context.Context, c *Client) error) {
	c.mu.Lock()
	c.onReAuth = f
	c.mu.Unlock()
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// reauthCtxKey marks the re-auth hook's own requests: they must never
// trigger another re-auth, or the refresh's leader would wait on itself.
type reauthCtxKey struct{}

// Do executes a GraphQL document. out may be nil for documents whose data we
// ignore. On an "access denied" GraphQL error the re-auth hook runs once and
// the request is replayed; if either fails the call returns
// driver.ErrNeedAuth.
func (c *Client) Do(ctx context.Context, query string, vars map[string]any, out any) error {
	err := c.roundTrip(ctx, query, vars, out)
	if !isAccessDenied(err) || c.onReAuth == nil || ctx.Value(reauthCtxKey{}) != nil {
		return err
	}
	hookErr, ok := c.reauthenticate(ctx)
	if !ok {
		// The request ctx expired while waiting for the refresh. Callers
		// classify results with errors.Is(…, driver.ErrNeedAuth), so the
		// bare "access denied" must still carry the sentinel here.
		return fmt.Errorf("%w: %w (re-auth wait: %w)", driver.ErrNeedAuth, err, ctx.Err())
	}
	if hookErr != nil {
		return fmt.Errorf("%w: %w", driver.ErrNeedAuth, hookErr)
	}
	err = c.roundTrip(ctx, query, vars, out)
	if isAccessDenied(err) {
		// The refresh succeeded but the replay is still denied: the session
		// is not recoverable with the stored credentials.
		return fmt.Errorf("%w: %w", driver.ErrNeedAuth, err)
	}
	return err
}

// reauthenticate refreshes the session token and reports whether the caller
// may replay its request. A refresh already in flight is waited on rather
// than duplicated, so a token expiring under N concurrent requests costs one
// token() call instead of N spurious failures. ok is false only when ctx
// expired while waiting; hookErr is the leader's refresh error (always nil
// for followers, whose replay decides).
func (c *Client) reauthenticate(ctx context.Context) (hookErr error, ok bool) {
	c.mu.Lock()
	if c.reauthDone != nil {
		done := c.reauthDone
		c.mu.Unlock()
		select {
		case <-done:
			return nil, true
		case <-ctx.Done():
			return nil, false
		}
	}
	done := make(chan struct{})
	c.reauthDone = done
	hook := c.onReAuth
	c.mu.Unlock()

	hookErr = hook(context.WithValue(context.Background(), reauthCtxKey{}, struct{}{}), c)

	c.mu.Lock()
	if c.reauthDone == done {
		c.reauthDone = nil
	}
	c.mu.Unlock()
	close(done)
	return hookErr, true
}

func (c *Client) roundTrip(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "dae-tui/0.1.0")
	if t := c.Token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", c.endpoint, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d: %.200s", c.endpoint, resp.StatusCode, raw)
	}

	var gr gqlResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return fmt.Errorf("decode response: %w: %.200s", err, raw)
	}
	if len(gr.Errors) > 0 {
		msg := gr.errorString()
		// A schema field unknown to the server almost always means the
		// running daemon is older than the installed package (upgrade
		// without restart). Surface that directly instead of a cryptic
		// validation error, and mark the error so the version-skew
		// fallbacks can detect it without message matching.
		if strings.Contains(msg, "Cannot query field") {
			return &unknownFieldError{msg: msg +
				"\n提示: 运行中的 daed 版本过旧 (升级后未重启?) — sudo systemctl restart daed"}
		}
		return errors.New(msg)
	}
	if out != nil && len(gr.Data) > 0 {
		if err := json.Unmarshal(gr.Data, out); err != nil {
			return fmt.Errorf("decode data: %w", err)
		}
	}
	return nil
}

func isAccessDenied(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "access denied")
}

// errUnknownField is the sentinel for GraphQL schema-validation failures:
// the running daemon does not know a field the query asked for (the installed
// package is newer than the daemon, or a fork chain dropped a field). The
// version-skew fallbacks in driver.go detect it with errors.Is instead of
// matching message text — daed rewording its validator output must not
// silently break the degradation paths.
var errUnknownField = errors.New("unknown graphql field")

// unknownFieldError carries the raw GraphQL error message (plus the restart
// hint) while still matching errUnknownField through errors.Is, so the
// user-visible text stays exactly what roundTrip assembled.
type unknownFieldError struct{ msg string }

func (e *unknownFieldError) Error() string   { return e.msg }
func (e *unknownFieldError) Is(t error) bool { return t == errUnknownField }

// --- public (unauthenticated) operations ---

func (c *Client) HealthCheck(ctx context.Context) error {
	var out struct {
		HealthCheck int `json:"healthCheck"`
	}
	return c.Do(ctx, qHealthCheck, nil, &out)
}

func (c *Client) NumberUsers(ctx context.Context) (int, error) {
	var out struct {
		NumberUsers int `json:"numberUsers"`
	}
	if err := c.Do(ctx, qNumberUsers, nil, &out); err != nil {
		return 0, err
	}
	return out.NumberUsers, nil
}

// FetchToken exchanges credentials for a JWT (valid 30 days on daed v2.1.1).
func (c *Client) FetchToken(ctx context.Context, username, password string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := c.Do(ctx, qToken, map[string]any{"username": username, "password": password}, &out)
	if err != nil {
		return "", err
	}
	return out.Token, nil
}

// CreateUser creates the first account and returns its JWT.
func (c *Client) CreateUser(ctx context.Context, username, password string) (string, error) {
	var out struct {
		CreateUser string `json:"createUser"`
	}
	err := c.Do(ctx, qCreateUser, map[string]any{"username": username, "password": password}, &out)
	if err != nil {
		return "", err
	}
	return out.CreateUser, nil
}
