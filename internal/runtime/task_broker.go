package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const brokerBodyLimit = 512 << 10

// ErrAttemptAlreadyHandled means the broker definitively reported that this
// duplicate/losing process must exit without invoking the handler.
var ErrAttemptAlreadyHandled = errors.New("mirrorstack: task attempt already claimed or terminal")

// BrokerClient isolates the versioned task broker HTTP wire contract.
type BrokerClient struct {
	baseURL string
	http    *http.Client
}

func NewBrokerClient(rawURL string, httpClient *http.Client) (*BrokerClient, error) {
	baseURL := strings.TrimSpace(rawURL)
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("mirrorstack: MS_TASK_BROKER_URL is invalid")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	} else if httpClient.Timeout == 0 {
		clone := *httpClient
		clone.Timeout = 15 * time.Second
		httpClient = &clone
	}
	return &BrokerClient{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}, nil
}

func (c *BrokerClient) Claim(ctx context.Context, jobID, attemptID, bootstrapToken string) (ClaimResponse, error) {
	var out ClaimResponse
	status, code, err := c.post(ctx, jobID, attemptID, "claim", bootstrapToken, nil, &out)
	if status == http.StatusConflict && code == "attempt_already_handled" {
		return ClaimResponse{}, ErrAttemptAlreadyHandled
	}
	if err != nil {
		return ClaimResponse{}, err
	}
	if out.Version != 1 {
		return ClaimResponse{}, fmt.Errorf("mirrorstack: task broker claim has unsupported version")
	}
	return out, nil
}

func (c *BrokerClient) Heartbeat(ctx context.Context, jobID, attemptID, token string) (HeartbeatResponse, error) {
	var out HeartbeatResponse
	_, _, err := c.post(ctx, jobID, attemptID, "heartbeat", token, struct {
		Version int    `json:"v"`
		Lease   string `json:"leaseToken"`
	}{1, token}, &out)
	if err == nil && out.Version != 1 {
		err = fmt.Errorf("mirrorstack: task broker heartbeat has unsupported version")
	}
	return out, err
}

func (c *BrokerClient) Refresh(ctx context.Context, jobID, attemptID, token string, kinds []string) (RefreshResponse, error) {
	var out RefreshResponse
	_, _, err := c.post(ctx, jobID, attemptID, "resources/refresh", token, struct {
		Version int      `json:"v"`
		Lease   string   `json:"leaseToken"`
		Kinds   []string `json:"kinds"`
	}{1, token, kinds}, &out)
	if err == nil && out.Version != 1 {
		err = fmt.Errorf("mirrorstack: task broker refresh has unsupported version")
	}
	return out, err
}

func (c *BrokerClient) Complete(ctx context.Context, jobID, attemptID, token string, result json.RawMessage) error {
	body := struct {
		Version int             `json:"v"`
		Lease   string          `json:"leaseToken"`
		Result  json.RawMessage `json:"result,omitempty"`
	}{1, token, result}
	return c.terminalPost(ctx, jobID, attemptID, "complete", token, body)
}

func (c *BrokerClient) Fail(ctx context.Context, jobID, attemptID, token, code, message string, retryable bool) error {
	body := struct {
		Version   int    `json:"v"`
		Lease     string `json:"leaseToken"`
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}{1, token, code, message, retryable}
	return c.terminalPost(ctx, jobID, attemptID, "fail", token, body)
}

// Terminal transitions are broker-idempotent. Retry only transport, throttling,
// and server failures so an ambiguous lost response cannot strand a completed
// attempt. Client/state conflicts remain hard errors.
func (c *BrokerClient) terminalPost(ctx context.Context, jobID, attemptID, suffix, token string, body any) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		status, _, err := c.post(ctx, jobID, attemptID, suffix, token, body, nil)
		if err == nil {
			return nil
		}
		lastErr = err
		if status != 0 && status != http.StatusTooManyRequests && status < 500 {
			return err
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (c *BrokerClient) post(ctx context.Context, jobID, attemptID, suffix, token string, body, out any) (int, string, error) {
	if token == "" {
		return 0, "", errors.New("mirrorstack: task broker capability is missing")
	}
	var reader io.Reader = http.NoBody
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		defer zeroBytes(encoded)
		reader = bytes.NewReader(encoded)
	}
	endpoint := c.baseURL + "/internal/tasks/" + url.PathEscape(jobID) + "/" + url.PathEscape(attemptID) + "/" + suffix
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		return 0, "", fmt.Errorf("mirrorstack: task broker request construction failed")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		return 0, "", fmt.Errorf("mirrorstack: task broker transport failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, brokerBodyLimit+1))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("mirrorstack: task broker response read failed")
	}
	defer zeroBytes(raw)
	if len(raw) > brokerBodyLimit {
		return resp.StatusCode, "", fmt.Errorf("mirrorstack: task broker response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Version int    `json:"v"`
			Code    string `json:"code"`
		}
		if err := json.Unmarshal(raw, &problem); err != nil || problem.Version != 1 || problem.Code == "" {
			return resp.StatusCode, "", fmt.Errorf("mirrorstack: task broker %s returned HTTP %d with malformed error envelope", suffix, resp.StatusCode)
		}
		return resp.StatusCode, problem.Code, fmt.Errorf("mirrorstack: task broker %s returned HTTP %d", suffix, resp.StatusCode)
	}
	if out != nil {
		if mediaType := strings.ToLower(resp.Header.Get("Content-Type")); mediaType != "" && !strings.HasPrefix(mediaType, "application/json") {
			return resp.StatusCode, "", fmt.Errorf("mirrorstack: task broker returned non-JSON content")
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if len(raw) == 0 || dec.Decode(out) != nil {
			return resp.StatusCode, "", fmt.Errorf("mirrorstack: task broker returned malformed JSON")
		}
		var trailing any
		if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
			return resp.StatusCode, "", fmt.Errorf("mirrorstack: task broker returned malformed JSON")
		}
	}
	return resp.StatusCode, "", nil
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
