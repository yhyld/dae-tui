package daed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"dae-tui/internal/driver"
)

// Client is a minimal GraphQL-over-HTTP client for the daed API: one POST
// endpoint, Bearer JWT auth, no websockets, no codegen.
type Client struct {
	endpoint string
	hc       *http.Client

	mu    sync.Mutex
	token string
	// onReAuth is invoked when the backend answers "access denied"; it
	// should refresh the token (e.g. re-run token() with stored
	// credentials) or return an error.
	onReAuth func(*Client) error
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
func (c *Client) SetReAuth(f func(*Client) error) {
	c.onReAuth = f
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// Do executes a GraphQL document. out may be nil for documents whose data we
// ignore. On an "access denied" GraphQL error the re-auth hook runs once and
// the request is replayed; if re-auth fails the call returns
// driver.ErrNeedAuth.
func (c *Client) Do(ctx context.Context, query string, vars map[string]any, out any) error {
	err := c.roundTrip(ctx, query, vars, out)
	if err == nil {
		return nil
	}
	if !isAccessDenied(err) || c.onReAuth == nil {
		return err
	}
	if rerr := c.onReAuth(c); rerr != nil {
		return fmt.Errorf("%w: %v", driver.ErrNeedAuth, rerr)
	}
	return c.roundTrip(ctx, query, vars, out)
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
		// validation error.
		if strings.Contains(msg, "Cannot query field") {
			return fmt.Errorf("%s\n提示: 运行中的 daed 版本过旧 (升级后未重启?) — sudo systemctl restart daed", msg)
		}
		return fmt.Errorf("%s", msg)
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
