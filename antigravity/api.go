// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/environments"
	gi "google.golang.org/genai/interactions/models/interactions"
	"google.golang.org/genai/interactions/models/operations"
	"google.golang.org/genai/interactions/retry"
)

// noRetries disables the SDK's retries for calls that must not be repeated.
var noRetries = operations.WithRetries(retry.Config{Strategy: "none"})

// client calls the Gemini API's environments and interactions endpoints
// through the Gemini API Go SDK.
type client struct {
	genai *genai.Client
}

// newClient returns a client for baseURL (the SDK's default when empty) that
// also trusts the PEM bundle at caFile, such as the AX egress gateway's.
func newClient(ctx context.Context, baseURL, apiKey, caFile string) (*client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA bundle %s: %w", caFile, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA bundle %s contains no valid PEM certificates", caFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	c, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: &http.Client{Transport: transport, Timeout: time.Minute},
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    baseURL,
			APIVersion: "v1beta",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating Gemini API client: %w", err)
	}
	return &client{genai: c}, nil
}

func (c *client) createEnvironment(ctx context.Context) (*environment, error) {
	resp, err := c.genai.Environments.CreateEnvironment(ctx, operations.CreateEnvironmentRequest{
		Body: environments.CreateEnvironmentRequest{Mode: gi.EnvironmentModeSelfHosted.ToPointer()},
	})
	if err != nil {
		return nil, fmt.Errorf("creating environment: %w", err)
	}
	env := resp.Environment
	if env == nil || env.ID == "" || env.EnvironmentKey == nil || *env.EnvironmentKey == "" {
		return nil, fmt.Errorf("creating environment: response has no id or environment_key")
	}
	return &environment{ID: strings.TrimPrefix(env.ID, "environments/"), Key: *env.EnvironmentKey}, nil
}

func (c *client) deleteEnvironment(ctx context.Context, id string) error {
	if _, err := c.genai.Environments.DeleteEnvironment(ctx, operations.DeleteEnvironmentRequest{ID: id}); err != nil {
		return fmt.Errorf("deleting environment %s: %w", id, err)
	}
	return nil
}

// createInteraction starts an agent interaction. The request is the JSON
// body, which is decoded into the SDK's request type.
func (c *client) createInteraction(ctx context.Context, request map[string]any) (string, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	var body gi.CreateAgentInteraction
	if err := json.Unmarshal(data, &body); err != nil {
		return "", fmt.Errorf("invalid interaction request: %w", err)
	}
	// Not retried: a retry after a lost response would start a second agent.
	resp, err := c.genai.Interactions.Create(ctx, operations.CreateInteractionRequest{
		Body: operations.NewCreateInteractionRequestBody(body),
	}, noRetries)
	if err != nil {
		return "", fmt.Errorf("creating interaction: %w", err)
	}
	if resp.Interaction == nil || resp.Interaction.ID == nil || *resp.Interaction.ID == "" {
		return "", fmt.Errorf("creating interaction: response has no id")
	}
	return *resp.Interaction.ID, nil
}

func (c *client) getInteraction(ctx context.Context, id string) (*interaction, error) {
	resp, err := c.genai.Interactions.Get(ctx, operations.GetInteractionByIDRequest{ID: id})
	if err != nil {
		return nil, fmt.Errorf("getting interaction %s: %w", id, err)
	}
	if resp.Interaction == nil {
		return nil, fmt.Errorf("getting interaction %s: empty response", id)
	}
	return newInteraction(id, resp.Interaction), nil
}

func (c *client) cancel(ctx context.Context, id string) error {
	if _, err := c.genai.Interactions.Cancel(ctx, operations.CancelInteractionByIDRequest{ID: id}, noRetries); err != nil {
		return fmt.Errorf("cancelling interaction %s: %w", id, err)
	}
	return nil
}

// interaction is the part of a Gemini API interaction the harness reads.
type interaction struct {
	ID                string
	Status            string
	OutputText        string
	ContinuationToken string
	Errors            []string
}

func newInteraction(id string, in *gi.Interaction) *interaction {
	it := &interaction{
		ID:                id,
		Status:            string(in.Status),
		OutputText:        deref(in.OutputText),
		ContinuationToken: deref(in.ContinuationToken),
	}
	for _, e := range in.Errors {
		if msg := deref(e.Message); msg != "" {
			it.Errors = append(it.Errors, msg)
		}
	}
	return it
}

// answer returns the text of the last model output, which the SDK
// collects into output_text.
func (it *interaction) answer() string {
	return it.OutputText
}

func (it *interaction) errorText() string {
	if len(it.Errors) == 0 {
		return fmt.Sprintf("interaction %s ended with status %s", it.ID, it.Status)
	}
	return strings.Join(it.Errors, "; ")
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
