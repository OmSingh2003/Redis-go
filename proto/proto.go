package proto

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// CmdType represents the type of command received from the client.
// It helps in dispatching the command to the correct handler.
type CmdType int

const (
	CmdUnknown CmdType = iota // Represents an unrecognized or unsupported command.
	CmdSet                     // Represents the SET command: SET key value.
	CmdGet                     // Represents the GET command: GET key.
	CmdPing                    // Represents the PING command: PING [message].
	CmdQuit                    // Represents the QUIT command: QUIT.
	CmdDel                     // Represents the DEL command: DEL key [key ...].
	CmdExists                  // Represents the EXISTS command: EXISTS key [key ...].
)

// ParsedCommand holds the details of a parsed RESP command.
// This structure makes it easier to access command details in the handler.
type ParsedCommand struct {
	Type        CmdType  // The enum type of the command (e.g., CmdSet, CmdGet).
	CommandName string   // The uppercase string representation of the command (e.g., "SET", "GET").
	Key         string   // Stores the first key, primarily for commands like SET, GET. For DEL/EXISTS, check Args.
	Val         string   // Stores the value, primarily for commands like SET.
	Args        [][]byte // Stores all arguments passed with the command (e.g., keys for DEL/EXISTS, or value for SET).
}

// parseArrayCount extracts the number of elements declared in a RESP array.
// Example: *3 -> 3
func parseArrayCount(line []byte) (int, error) {
	// Must start with '*' and have at least one digit.
	if len(line) < 2 || line[0] != '*' {
		return 0, errors.New("invalid array format: missing '*' prefix or too short")
	}
	count, err := strconv.Atoi(string(line[1:]))
	if err != nil {
		// If Atoi fails, it means the part after '*' wasn't a valid number.
		return 0, fmt.Errorf("invalid array count value: %s", string(line[1:]))
	}
	return count, nil
}

// parseBulkStringLength extracts the length of a RESP bulk string.
// Example: $5 -> 5
func parseBulkStringLength(line []byte, r *bytes.Reader) (int, error) {
	// Must start with '$'.
	if len(line) == 0 || line[0] != '$' {
		return 0, fmt.Errorf("expected bulk string length prefix '$', got %q", line)
	}
	length, err := strconv.Atoi(string(line[1:]))
	if err != nil {
		// If Atoi fails, the part after '$' wasn't a valid number.
		return 0, fmt.Errorf("invalid bulk string length: %w", err)
	}
	return length, nil
}

// readLine reads a single line ending with CRLF (\r\n) from the bytes.Reader.
// This is a helper for parsing RESP components.
func readLine(r *bytes.Reader) ([]byte, error) {
	var line []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			// If we hit EOF and have read some bytes, it might be an incomplete command.
			// If line is empty, it's just EOF.
			if err == io.EOF && len(line) > 0 {
				return line, errors.New("incomplete line, missing CRLF before EOF")
			}
			return nil, err // Genuine EOF or other read error.
		}
		line = append(line, b)
		// Check if the line ends with CRLF.
		if len(line) >= 2 && line[len(line)-2] == '\r' && line[len(line)-1] == '\n' {
			return bytes.TrimSpace(line), nil // Return the line without CRLF.
		}
	}
}

// ParseCommand parses the raw byte data (expected to be in RESP format) into a ParsedCommand.
// This is the main entry point for understanding client requests.
func ParseCommand(rawData []byte) (*ParsedCommand, error) {
	// Using bytes.Reader for convenient ReadByte, Read, etc. operations.
	reader := bytes.NewReader(rawData)
	parsedCmd := &ParsedCommand{}

	// Read the first line to determine the command structure (array or inline).
	firstLine, err := readLine(reader)
	if err != nil {
		// If we can't even read the first line, it's a significant parsing error.
		return nil, fmt.Errorf("error reading first line: %w", err)
	}

	// Check if it's an array (standard way for commands with arguments).
	if firstLine[0] == '*' {
		arrayLen, err := parseArrayCount(firstLine)
		if err != nil {
			return nil, fmt.Errorf("failed to parse array length: %w", err)
		}
		if arrayLen <= 0 {
			return nil, errors.New("invalid command: array length must be positive")
		}

		// We'll store all parts of the array (command + args) here.
		parts := make([][]byte, arrayLen)
		for i := 0; i < arrayLen; i++ {
			// Each part of a command array must be a bulk string.
			lenLine, err := readLine(reader)
			if err != nil {
				return nil, fmt.Errorf("error reading bulk string length line for element %d: %w", i, err)
			}
			strLen, err := parseBulkStringLength(lenLine, reader)
			if err != nil {
				return nil, fmt.Errorf("error parsing bulk string length for element %d: %w", i, err)
			}

			if strLen == -1 { // Null bulk string.
				parts[i] = nil // Represent as nil, handlers should be aware.
				// We still need to consume the CRLF if any reader advances past it.
				// readLine for bulk string data itself would handle this if strLen > 0.
				// For -1, the length line is all there is.
			} else {
				// Read the bulk string data itself.
				data := make([]byte, strLen)
				n, err := io.ReadFull(reader, data)
				if err != nil || n != strLen {
					return nil, fmt.Errorf("error reading bulk string data for element %d (expected %d, got %d): %w", i, strLen, n, err)
				}
				parts[i] = data

				// Consume the trailing CRLF after the bulk string data.
				crlfLine, err := readLine(reader)
				if err != nil {
					return nil, fmt.Errorf("error reading CRLF after bulk string data for element %d: %w", i, err)
				}
				if len(crlfLine) != 0 { // Should be an empty line after trimming.
					return nil, fmt.Errorf("expected empty line (CRLF) after bulk string data, got %q for element %d", crlfLine, i)
				}
			}
		}
		// The first part is the command name.
		parsedCmd.CommandName = strings.ToUpper(string(parts[0]))
		// The rest are arguments.
		if arrayLen > 1 {
			parsedCmd.Args = parts[1:]
			// For convenience, if Key/Val are relevant, populate them.
			// This is a simplification; complex commands might need more nuanced arg handling.
			if len(parts[1:]) > 0 {
				parsedCmd.Key = string(parts[1]) // First arg as Key
			}
			if len(parts[1:]) > 1 {
				// For SET, the second arg (parts[2]) is the value.
				// For other commands, this might not be 'Val' in the traditional sense.
				// This is a heuristic.
				if parsedCmd.CommandName == "SET" { // Only populate Val for SET.
					parsedCmd.Val = string(parts[2])
				}
			}
		}

	} else { // Inline command (less common for complex commands, mainly for PING, QUIT by some clients).
		// Treat the first line as the command and potential arguments, space-separated.
		// This is a simplified handling for inline commands.
		// Redis typically uses arrays for almost everything from clients.
		fields := bytes.Fields(firstLine)
		if len(fields) == 0 {
			return nil, errors.New("empty inline command")
		}
		parsedCmd.CommandName = strings.ToUpper(string(fields[0]))
		if len(fields) > 1 {
			// Store remaining fields as Args.
			// Convert them to [][]byte for consistency with array parsing.
			parsedCmd.Args = make([][]byte, len(fields)-1)
			for i, field := range fields[1:] {
				parsedCmd.Args[i] = field
			}
			parsedCmd.Key = string(fields[1]) // First arg as Key
			// Val is not typically set for inline commands other than perhaps a conceptual SET.
		}
	}

	// Determine the command type enum.
	switch parsedCmd.CommandName {
	case "SET":
		parsedCmd.Type = CmdSet
	case "GET":
		parsedCmd.Type = CmdGet
	case "PING":
		parsedCmd.Type = CmdPing
	case "QUIT":
		parsedCmd.Type = CmdQuit
	case "DEL": // Our new command!
		parsedCmd.Type = CmdDel
	case "EXISTS": // And another one!
		parsedCmd.Type = CmdExists
	default:
		parsedCmd.Type = CmdUnknown // If the command name isn't recognized.
	}

	return parsedCmd, nil
}
