package main

import (
	"bytes"
	"fmt"
	"io"
	"log" // Still here for now, but ideally errors are returned

	"github.com/tidwall/resp"
)

// 1. Rename types
type CommandType int

const (
	CmdSet CommandType = iota
	CmdGet
	// ... other command types
)

type ParsedCommand struct {
	Type CommandType
	Key  string
	Val  string // Only relevant for some commands like SET
	Args []string // For more general commands
}

// 2. Fix function signature, parameter, and return path
func parseCommand(rawCommandData []byte) (*ParsedCommand, error) { // Changed parameter, return type
	if len(rawCommandData) == 0 {
		return nil, fmt.Errorf("empty command data")
	}
	rd := resp.NewReader(bytes.NewBuffer(rawCommandData)) // Use bytes.NewBuffer

	// Expecting a single top-level RESP value representing the command array
	v, _, err := rd.ReadValue()
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("incomplete command: %w", err) // EOF before a full command is an error
		}
		return nil, fmt.Errorf("failed to read RESP value: %w", err) // 6. Return errors
	}

	// 5. Logic to actually build a command
	if v.Type() != resp.Array {
		return nil, fmt.Errorf("command must be a RESP array, got %s", v.Type())
	}

	arr := v.Array()
	if len(arr) == 0 {
		return nil, fmt.Errorf("empty command array")
	}

	cmd := &ParsedCommand{}
	commandName := arr[0].String() // Assuming first element is command name

	fmt.Printf("Read Command Array (Type: %s):\n", v.Type())
	for i, val := range arr {
		fmt.Printf("  #%d Type: %s, Value: %s\n", i, val.Type(), val.String())
	}

	// Example: Simple SET command parsing
	// You'd need more robust logic here for different commands
	switch commandName {
	case "SET":
		if len(arr) != 3 {
			return nil, fmt.Errorf("SET command requires 2 arguments (key value), got %d", len(arr)-1)
		}
		cmd.Type = CmdSet
		cmd.Key = arr[1].String()
		cmd.Val = arr[2].String()
	case "GET":
		if len(arr) != 2 {
			return nil, fmt.Errorf("GET command requires 1 argument (key), got %d", len(arr)-1)
		}
		cmd.Type = CmdGet
		cmd.Key = arr[1].String()
	default:
		return nil, fmt.Errorf("unknown command: %s", commandName)
	}

	// Check for trailing data after the first command, if this function should only parse one.
	_, _, err = rd.ReadValue()
	if err != io.EOF {
		// If there's more data or an error other than EOF, it means there's unexpected trailing data
		// or the input wasn't just a single command.
		// This depends on whether your protocol expects one command per ReadValue call
		// or a stream. For simplicity, assuming one command per call here.
		if err != nil { // A real error reading further
			log.Printf("Warning: Error checking for trailing data: %v (ignoring)", err) // Or handle as strict error
		} else { // No error, but also not EOF means more data
			log.Printf("Warning: Trailing data found after command (ignoring)")
		}
	}


	return cmd, nil // Return the constructed command and nil error
}
