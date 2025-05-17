package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	// "strings" // May not be needed with revised error handling
)

type Peer struct {
	conn  net.Conn
	msgCh chan Message
}

func NewPeer(conn net.Conn, msgCh chan Message) *Peer {
	return &Peer{
		conn:  conn,
		msgCh: msgCh,
	}
}

func (p *Peer) readLoop() error {
	reader := bufio.NewReader(p.conn)
	for {
		// Read one full RESP command
		commandBytes, err := readRESPCommand(reader)
		if err != nil {
			// Handle different types of errors gracefully
			if errors.Is(err, io.EOF) {
				slog.Info("Client closed connection (EOF)", "remoteAddr", p.conn.RemoteAddr())
			} else if errors.Is(err, net.ErrClosed) {
				slog.Info("Connection closed by peer or server", "remoteAddr", p.conn.RemoteAddr(), "errType", fmt.Sprintf("%T", err))
			} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				slog.Info("Client connection timeout", "remoteAddr", p.conn.RemoteAddr())
			} else {
				slog.Error("Error reading RESP command from peer", "err", err, "remoteAddr", p.conn.RemoteAddr())
			}
			return err // Terminate readLoop on any error, signaling connection closure
		}

		// It's unlikely to get here with commandBytes being empty if err is nil, but as a safe guard.
		if len(commandBytes) == 0 {
			continue
		}

		// For debugging, you might want to see the raw command. Be careful with very large commands in logs.
		// slog.Debug("Raw command received", "remoteAddr", p.conn.RemoteAddr(), "dataLength", len(commandBytes), "dataHex", fmt.Sprintf("%x", commandBytes))

		p.msgCh <- Message{
			conn: p.conn, // Pass the original connection for response writing and peer identification
			data: commandBytes,
		}
	}
}

// readRESPCommand reads a complete RESP message from the reader.
// A complete RESP message for a command is typically an Array or an inline command.
func readRESPCommand(r *bufio.Reader) ([]byte, error) {
	var cmdBuffer bytes.Buffer
	// The top-level value of a command is parsed here.
	// This will typically be an Array for most Redis commands, or an inline command.
	err := parseRESPValue(r, &cmdBuffer)
	if err != nil {
		return nil, err
	}
	return cmdBuffer.Bytes(), nil
}

// parseRESPValue recursively parses a single RESP value (Simple String, Error, Integer, Bulk String, Array)
// and appends its raw byte representation (including prefixes, data, and CRLFs) to cmdBuffer.
func parseRESPValue(r *bufio.Reader, cmdBuffer *bytes.Buffer) error {
	typeByte, err := r.ReadByte() // Read the first byte indicating the type
	if err != nil {
		return err // Handles EOF or other read errors
	}
	cmdBuffer.WriteByte(typeByte) // Write the type byte to our command buffer

	switch typeByte {
	case '+', '-', ':': // Simple String, Error, Integer
		// The content for these types is the rest of the line up to CRLF.
		line, err := readLineCRLF(r)
		if err != nil {
			return fmt.Errorf("resp: failed to read line for type %c: %w", typeByte, err)
		}
		cmdBuffer.Write(line) // line already includes CRLF
		return nil

	case '$': // Bulk String
		// The line after '$' contains the length of the string.
		lenLine, err := readLineCRLF(r) // lenLine includes CRLF, e.g., "5\r\n"
		if err != nil {
			return fmt.Errorf("resp: failed to read length line for bulk string: %w", err)
		}
		cmdBuffer.Write(lenLine)

		// Parse the length, excluding the trailing CRLF from lenLine
		length, err := strconv.ParseInt(string(lenLine[:len(lenLine)-2]), 10, 64)
		if err != nil {
			return fmt.Errorf("resp: invalid bulk string length '%s': %w", string(lenLine[:len(lenLine)-2]), err)
		}

		if length < -1 { // Length can be -1 for Null Bulk String, but not less.
			return fmt.Errorf("resp: invalid bulk string length %d", length)
		}
		if length == -1 { // Null Bulk String (e.g., "$-1\r\n"). Nothing more to read.
			return nil
		}

		// Read the bulk string data (length bytes) followed by CRLF (2 bytes).
		bulkDataWithCRLF := make([]byte, int(length)+2)
		if _, err := io.ReadFull(r, bulkDataWithCRLF); err != nil {
			// This can be io.EOF or io.ErrUnexpectedEOF if the stream ends prematurely.
			return fmt.Errorf("resp: error reading bulk string data (expected %d bytes + CRLF): %w", length, err)
		}

		// Verify that the bulk data actually ends with CRLF.
		if bulkDataWithCRLF[len(bulkDataWithCRLF)-2] != '\r' || bulkDataWithCRLF[len(bulkDataWithCRLF)-1] != '\n' {
			return errors.New("resp: bulk string data not CRLF terminated")
		}
		cmdBuffer.Write(bulkDataWithCRLF)
		return nil

	case '*': // Array
		// The line after '*' contains the number of elements in the array.
		countLine, err := readLineCRLF(r) // countLine includes CRLF, e.g., "3\r\n"
		if err != nil {
			return fmt.Errorf("resp: failed to read count line for array: %w", err)
		}
		cmdBuffer.Write(countLine)

		// Parse the count, excluding the trailing CRLF from countLine
		count, err := strconv.ParseInt(string(countLine[:len(countLine)-2]), 10, 64)
		if err != nil {
			return fmt.Errorf("resp: invalid array count '%s': %w", string(countLine[:len(countLine)-2]), err)
		}

		if count < -1 { // Count can be -1 for Null Array, but not less.
			return fmt.Errorf("resp: invalid array count %d", count)
		}
		if count == -1 { // Null Array (e.g., "*-1\r\n"). Nothing more to read.
			return nil
		}

		// Read 'count' number of RESP values recursively. Each of these values is a full RESP element.
		for i := 0; i < int(count); i++ {
			if err := parseRESPValue(r, cmdBuffer); err != nil { // Recursive call
				return fmt.Errorf("resp: error reading array element %d of %d: %w", i, count, err)
			}
		}
		return nil

	default:
		// This case handles data that doesn't start with a known RESP prefix ('+', '-', ':', '$', '*').
		// This could be an "inline" command (e.g., "PING\r\n" or "QUIT\r\n" sent by some clients like redis-cli).
		// The `typeByte` we read is the first character of this inline command.
		// We need to read the rest of the line up to CRLF.

		// `typeByte` is already in cmdBuffer. Read the rest of the line.
		// `ReadBytes` reads until the delimiter is found, and includes it in the returned slice.
		restOfLine, err := r.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("resp: failed to read inline command continuation: %w", err)
		}
		cmdBuffer.Write(restOfLine) // Append the rest of the line (e.g., "ING\r\n" for PING)

		// Verify that what we have (typeByte + restOfLine) ends with CRLF.
		// The cmdBuffer now contains the full supposed inline command.
		fullInlineCmd := cmdBuffer.Bytes() // Get current content of buffer for check
		// We need to re-check because typeByte was written separately. The part written by `cmdBuffer.Write(restOfLine)` is `restOfLine`.
		// So the actual check is on `typeByte` + `restOfLine`.
		// The `cmdBuffer` itself now holds the full line including the `typeByte` and `restOfLine`.
		if len(fullInlineCmd) < (1+2) || fullInlineCmd[len(fullInlineCmd)-2] != '\r' || fullInlineCmd[len(fullInlineCmd)-1] != '\n' {
			// This means something like "P\n" was sent, or "PING\n" without \r
			return errors.New("resp: inline command not CRLF terminated correctly")
		}
		slog.Debug("Handled as potential inline command", "command", string(fullInlineCmd))
		return nil
	}
}

// readLineCRLF reads a line from bufio.Reader that is expected to end with CRLF.
// It returns the line including the CRLF.
// If an error occurs, or if the line is not CRLF terminated, an error is returned.
func readLineCRLF(r *bufio.Reader) ([]byte, error) {
	// Reads until the first occurrence of '\n' in the input,
	// returning a slice containing the data up to and including the delimiter.
	line, err := r.ReadBytes('\n')
	if err != nil {
		// This could be io.EOF if the stream ends before '\n'.
		// Or another error if the read fails.
		return nil, err
	}

	// Check if the line (which includes '\n') is at least 2 bytes long and ends with '\r\n'.
	if len(line) < 2 || line[len(line)-2] != '\r' {
		// This can happen if the client sends just '\n' or some other malformed line.
		return nil, errors.New("resp: line not terminated with CRLF")
	}
	return line, nil
}
