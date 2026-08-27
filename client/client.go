// Package client provides a typed client for the Hearsay daemon API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sirrobot01/hearsay"
)

const maxResponseSize = 64 << 20

// Client calls one authenticated Hearsay daemon.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// New creates a client with a 30-second HTTP timeout.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Answer is the daemon wire representation of one claim.
type Answer struct {
	Subject        string  `json:"subject,omitempty"`
	Known          bool    `json:"known"`
	Value          any     `json:"value"`
	Support        float64 `json:"support"`
	EvidenceWeight float64 `json:"evidence_weight"`
	Sources        int     `json:"sources"`
	AgeSeconds     int64   `json:"age_seconds"`
	Coverage       float64 `json:"coverage"`
	Local          bool    `json:"local,omitzero"`
}

// BoolValue returns a known Boolean value.
func (a Answer) BoolValue() (bool, bool) {
	value, ok := a.Value.(bool)
	return value, a.Known && ok
}

// ScalarValue returns a known scalar value.
func (a Answer) ScalarValue() (float64, bool) {
	value, ok := a.Value.(float64)
	return value, a.Known && ok
}

// PairedAnswer preserves complementary positive and negative evidence.
type PairedAnswer struct {
	Subject  string                `json:"subject"`
	State    hearsay.EvidenceState `json:"state"`
	Positive Answer                `json:"positive"`
	Negative Answer                `json:"negative"`
}

// Observation is ground truth learned during the host's normal work.
type Observation struct {
	Subject    string    `json:"subject"`
	Value      any       `json:"value"`
	ObservedAt time.Time `json:"observed_at,omitzero"`
}

// Namespace describes one configured daemon capability.
type Namespace struct {
	Name              string `json:"namespace"`
	ValueType         string `json:"value_type"`
	FreshnessSeconds  int64  `json:"freshness_seconds"`
	TimeBucketSeconds int64  `json:"time_bucket_seconds,omitzero"`
}

// Claims queries a batch in input order.
func (c *Client) Claims(ctx context.Context, namespace string, subjects []string) ([]Answer, error) {
	request := struct {
		Namespace string   `json:"ns"`
		Subjects  []string `json:"subjects"`
	}{Namespace: namespace, Subjects: subjects}
	var response struct {
		Answers []Answer `json:"answers"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/claims", request, &response); err != nil {
		return nil, err
	}
	return response.Answers, nil
}

// Evidence resolves complementary Boolean namespaces for a batch.
func (c *Client) Evidence(ctx context.Context, positiveNS, negativeNS string, subjects []string) ([]PairedAnswer, error) {
	request := struct {
		PositiveNS string   `json:"positive_ns"`
		NegativeNS string   `json:"negative_ns"`
		Subjects   []string `json:"subjects"`
	}{PositiveNS: positiveNS, NegativeNS: negativeNS, Subjects: subjects}
	var response struct {
		Answers []PairedAnswer `json:"answers"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/evidence", request, &response); err != nil {
		return nil, err
	}
	return response.Answers, nil
}

// Observe submits ground truth learned as a byproduct of host work.
func (c *Client) Observe(ctx context.Context, namespace string, observations []Observation) (int, error) {
	request := struct {
		Namespace    string        `json:"ns"`
		Observations []Observation `json:"observations"`
	}{Namespace: namespace, Observations: observations}
	var response struct {
		Accepted int `json:"accepted"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/observations", request, &response); err != nil {
		return 0, err
	}
	return response.Accepted, nil
}

// Report closes the feedback loop after acting on a hint.
func (c *Client) Report(ctx context.Context, namespace, subject string, outcome any) error {
	request := struct {
		Namespace string `json:"ns"`
		Subject   string `json:"subject"`
		Outcome   any    `json:"outcome"`
	}{Namespace: namespace, Subject: subject, Outcome: outcome}
	return c.doJSON(ctx, http.MethodPost, "/v1/outcomes", request, nil)
}

// Namespaces returns the protocol version and configured capabilities.
func (c *Client) Namespaces(ctx context.Context) (string, []Namespace, error) {
	var response struct {
		Protocol   string      `json:"protocol"`
		Namespaces []Namespace `json:"namespaces"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/namespaces", nil, &response); err != nil {
		return "", nil, err
	}
	return response.Protocol, response.Namespaces, nil
}

// HTTPError is a non-2xx daemon response.
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("hearsay daemon returned %d: %s", e.StatusCode, e.Message)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, output any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + path
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return err
	}
	if len(raw) > maxResponseSize {
		return fmt.Errorf("hearsay daemon response exceeds %d bytes", maxResponseSize)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(raw))
		var payload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &payload) == nil && payload.Error != "" {
			message = payload.Error
		}
		return &HTTPError{StatusCode: response.StatusCode, Message: message}
	}
	if output == nil {
		return nil
	}
	return json.Unmarshal(raw, output)
}
