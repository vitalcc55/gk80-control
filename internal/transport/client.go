// Package transport implements the native authenticated core connection.
package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
)

type Client struct {
	endpoint string
	secret   string
	http     *http.Client
}

// NewClient pins the server certificate before sending the native-process secret.
func NewClient(address string, fingerprint [32]byte, secret string) (*Client, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || secret == "" {
		return nil, errors.New("invalid core endpoint")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid core port")
	}
	if fingerprint == [32]byte{} {
		return nil, errors.New("missing core certificate fingerprint")
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The private, per-process certificate is authenticated by its pinned hash below.
		InsecureSkipVerify: true, //nolint:gosec
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("core certificate missing")
			}
			actual := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(actual[:], fingerprint[:]) != 1 {
				return errors.New("core certificate mismatch")
			}
			return nil
		},
	}
	return &Client{
		endpoint: "https://" + net.JoinHostPort(host, portText), secret: secret,
		http: &http.Client{
			Transport:     &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig},
			Timeout:       3 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Handshake validates required fields instead of treating a decoded zero value as success.
func (c *Client) Handshake(ctx context.Context) (contract.Handshake, error) {
	var result contract.Handshake
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/v1/handshake", nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	response, err := c.http.Do(req)
	if err != nil {
		return result, fmt.Errorf("core handshake unavailable: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return result, errors.New("core handshake rejected")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || len(body) > 8192 {
		return result, errors.New("invalid core handshake response")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return contract.Handshake{}, errors.New("invalid core handshake response")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return contract.Handshake{}, errors.New("invalid core handshake response")
	}
	if result.ApiVersion != "v1" || !result.Demo || result.BackendRunId == uuid.Nil {
		return contract.Handshake{}, errors.New("unsupported core identity")
	}
	return result, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }
