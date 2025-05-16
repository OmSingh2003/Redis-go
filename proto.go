package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog" 

	"github.com/tidwall/resp"
)


type CommandType int

const (
	CmdUnknown CommandType = iota 
	CmdSet
	CmdGet

)

type ParsedCommand struct {
	Type        CommandType
	CommandName string 
	Key         string
	Val         string  
	Args        []string 
}

func parseCommand(rawCommandData []byte) (*ParsedCommand, error) {
	if len(rawCommandData) == 0 {
		return nil, fmt.Errorf("empty command data")
	}
	rd := resp.NewReader(bytes.NewBuffer(rawCommandData))

	v, _, err := rd.ReadValue()
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("incomplete command: %w", err)
		}
		return nil, fmt.Errorf("failed to read RESP value: %w", err)
	}

	if v.Type() != resp.Array {
		return nil, fmt.Errorf("command must be a RESP array, got %s", v.Type())
	}

	arr := v.Array()
	if len(arr) == 0 {
		return nil, fmt.Errorf("empty command array")
	}

	cmd := &ParsedCommand{}
	cmd.CommandName = arr[0].String() 


	switch cmd.CommandName { 
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
		slog.Warn("Unknown command encountered during parsing", "commandName", cmd.CommandName)
		cmd.Type = CmdUnknown
	}
	_, _, err = rd.ReadValue()
	if err != io.EOF {
		if err != nil {
			slog.Warn("Error checking for trailing data after command", "error", err)
		} else {
			slog.Warn("Trailing data found after command, indicates multiple commands in one read or malformed input.")
		}
	}

	return cmd, nil
}
