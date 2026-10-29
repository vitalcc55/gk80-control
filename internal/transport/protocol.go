package transport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/google/uuid"
)

// Bootstrap travels only through the child process's private input pipe.
type Bootstrap struct {
	Secret     string `json:"secret"`
	ProfileDir string `json:"profileDir"`
	ProfileID  string `json:"profileId"`
	AppVersion string `json:"appVersion"`
}

// Ready travels only through the child process's private output pipe.
type Ready struct {
	ErrorCode    string    `json:"errorCode,omitempty"`
	Address      string    `json:"address"`
	Fingerprint  string    `json:"fingerprint"`
	APIVersion   string    `json:"apiVersion"`
	BackendRunID uuid.UUID `json:"backendRunId"`
	Demo         bool      `json:"demo"`
}

// ReadFrame bounds bootstrap data and preserves the remaining owner channel.
func ReadFrame(reader *bufio.Reader, value interface{}) error {
	frame, err := reader.ReadSlice('\n')
	if err != nil || len(frame) > 8192 {
		return errors.New("invalid bootstrap frame")
	}
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid bootstrap frame")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("invalid bootstrap frame")
	}
	return nil
}
