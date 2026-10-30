package desktop

import (
	"bytes"
	"encoding/json"
	"sync"

	"github.com/vitalcc55/gk80-control/internal/contract"
)

var inputValidation struct {
	once      sync.Once
	validator *contract.InputValidator
	err       error
}

type commandInput struct {
	body     []byte
	identity []byte
	valid    bool
}

func inspectWailsCommand(name string, body []byte, target any) (commandInput, error) {
	inputValidation.once.Do(func() { inputValidation.validator, inputValidation.err = contract.NewInputValidator() })
	if inputValidation.err != nil {
		return commandInput{}, inputValidation.err
	}
	inspected, err := inputValidation.validator.Inspect(name, body, target)
	if err != nil {
		return commandInput{}, err
	}
	return commandInput{body: append([]byte(nil), body...), identity: inspected.Identity, valid: inspected.Valid}, nil
}

// CreateCommandInput preserves the JSON that Wails received before Go assigns zero values.
type CreateCommandInput struct {
	contract.CreateTestCommand
	commandInput
	lossless bool
}

func (c *CreateCommandInput) UnmarshalJSON(body []byte) error {
	var command contract.CreateTestCommand
	input, err := inspectWailsCommand("CreateTestCommand", body, &command)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		return err
	}
	canonical, err := contract.CanonicalJSON(encoded)
	if err != nil {
		return err
	}
	*c = CreateCommandInput{CreateTestCommand: command, commandInput: input, lossless: bytes.Equal(input.identity, canonical)}
	return nil
}

type StartCommandInput struct {
	contract.StartTestCommand
	commandInput
}

func (c *StartCommandInput) UnmarshalJSON(body []byte) error {
	var command contract.StartTestCommand
	input, err := inspectWailsCommand("StartTestCommand", body, &command)
	if err != nil {
		return err
	}
	*c = StartCommandInput{StartTestCommand: command, commandInput: input}
	return nil
}

type ConfirmCommandInput struct {
	contract.ConfirmActionCommand
	commandInput
}

func (c *ConfirmCommandInput) UnmarshalJSON(body []byte) error {
	var command contract.ConfirmActionCommand
	input, err := inspectWailsCommand("ConfirmActionCommand", body, &command)
	if err != nil {
		return err
	}
	*c = ConfirmCommandInput{ConfirmActionCommand: command, commandInput: input}
	return nil
}

type StopCommandInput struct {
	contract.StopTestCommand
	commandInput
}

func (c *StopCommandInput) UnmarshalJSON(body []byte) error {
	var command contract.StopTestCommand
	input, err := inspectWailsCommand("StopTestCommand", body, &command)
	if err != nil {
		return err
	}
	*c = StopCommandInput{StopTestCommand: command, commandInput: input}
	return nil
}
