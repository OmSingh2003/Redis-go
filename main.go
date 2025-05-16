package main

import (
	"bytes"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"sync"
	"strings"
	"goredis/proto" // Assuming your proto package is in this path
)

const defaultListenAdrr = ":5005" //Deafult listening Address (if client does not specify the port this will be used )

type Config struct { // config structure
	ListenAddress string //store address and port
}

type Message struct {
	conn net.Conn //network connection (client)
	data []byte   //redis cmd
}

type Server struct {
	Config        // inherits ListenAddress
	peers     map[*Peer]bool //keep track of connected clients
	ln        net.Listener   //TCP listener returned by net.Listen
	addPeerCh chan *Peer     // Channel for adding peers
	delPeerCh chan *Peer     // channel for deleting peers
	quitCh    chan struct{}  // signal to shutdown the server gracefully
	msgCh     chan Message   //channel where incoming messages structs are sent.

	mu   sync.RWMutex       // prevents concurrent access to the in-memory data store
	data map[string][]byte // key value store in (Redis in-memory database)
}

func NewServer(cfg Config) *Server { //intializing server instance
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = defaultListenAdrr // if no ListenAddress is stated use the default one
	}
	// initializing server fields
	return &Server{
		Config:    cfg,
		peers:     make(map[*Peer]bool),
		addPeerCh: make(chan *Peer),
		delPeerCh: make(chan *Peer),
		quitCh:    make(chan struct{}),
		msgCh:     make(chan Message),
		data:      make(map[string][]byte),
	}
}

// Start the server ,Listens for incoming TCP connections, and runs connections/message handling loops.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.ListenAddress) // Binds to the configured TCP address
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.ListenAddress, err)
	}
	s.ln = ln // Saves the listener to the server struct

	go s.loop() // goroutine for the main server loop

	slog.Info("Server running", "ListenAddress", s.ListenAddress) //log server startup message
	slog.Info("Accepting connections...")
	go s.acceptLoop() //accepts new client connections

	return nil
}

// keep log output clean by removing \r and \n and avoid injection/newline
func SanitizeErrorMessage(msg string) string {
	var sanitized bytes.Buffer
	for _, r := range msg {
		if r != '\r' && r != '\n' {
			sanitized.WriteRune(r)
		}
	}
	return sanitized.String()
}

// handleMessage is the heart of command processing.
// It parses the raw message data and dispatches to the appropriate command logic.
func (s *Server) handleMessage(msg Message) {
	// Attempt to parse the command from the raw data.
	// parseCommand is expected to be in our proto package now.
	parsedCmd, err := proto.ParseCommand(msg.data) // Command Parsing
	if err != nil {
		// If parsing fails, we log it and send an error back to the client.
		slog.Error("Failed to parse command", "err", err, "remoteAddr", msg.conn.RemoteAddr(), "rawData", string(msg.data))
		errorResponse := []byte(fmt.Sprintf("-ERR %s\r\n", SanitizeErrorMessage(err.Error())))
		_, writeErr := msg.conn.Write(errorResponse)
		if writeErr != nil {
			// If writing the error response fails, not much more we can do for this client.
			slog.Error("Failed to write error response to client", "err", writeErr, "remoteAddr", msg.conn.RemoteAddr())
		}
		return
	}

	// Successfully parsed, now let's log what we're about to do.
	slog.Debug("Executing command",
		"commandName", parsedCmd.CommandName,
		// "key" and "argsCount" logging might need adjustment based on how ParsedCommand is populated
		// For DEL/EXISTS, all keys are in Args.
		"argsCount", len(parsedCmd.Args),
		"remoteAddr", msg.conn.RemoteAddr())

	var response []byte
	var closeConnectionAfterResponse bool // Flag to indicate if we should close the connection (e.g., for QUIT).

	// This switch dispatches commands based on their parsed type.
	switch parsedCmd.Type {

	// SET <key> <value>
	// Stores the value in the in-memory key-value store.
	case proto.CmdSet:
		// SET command needs exactly two arguments (key and value).
		if len(parsedCmd.Args) != 2 {
			slog.Warn("SET command with wrong number of arguments", "argsReceived", len(parsedCmd.Args), "remoteAddr", msg.conn.RemoteAddr())
			response = []byte(fmt.Sprintf("-ERR wrong number of arguments for '%s' command\r\n", strings.ToLower(parsedCmd.CommandName)))
		} else {
			// Args[0] is the key, Args[1] is the value for SET.
			key := string(parsedCmd.Args[0])
			value := parsedCmd.Args[1] // Value is stored as []byte

			s.mu.Lock() // Acquire write lock to modify the data map.
			s.data[key] = value
			s.mu.Unlock() // Release the lock.

			slog.Info("SET executed", "key", key, "remoteAddr", msg.conn.RemoteAddr())
			response = []byte("+OK\r\n") // Standard Redis OK response.
		}

	// GET <key>
	// Fetches the value of the given key if it exists.
	case proto.CmdGet:
		// GET command needs exactly one argument (the key).
		if len(parsedCmd.Args) != 1 {
			slog.Warn("GET command with wrong number of arguments", "argsReceived", len(parsedCmd.Args), "remoteAddr", msg.conn.RemoteAddr())
			response = []byte(fmt.Sprintf("-ERR wrong number of arguments for '%s' command\r\n", strings.ToLower(parsedCmd.CommandName)))
		} else {
			key := string(parsedCmd.Args[0]) // The key to fetch.

			s.mu.RLock() // Acquire read lock as we are only reading.
			value, ok := s.data[key]
			s.mu.RUnlock() // Release the read lock.

			if ok {
				// Key exists, send it back as a bulk string.
				slog.Info("GET executed", "key", key, "valueFound", true, "remoteAddr", msg.conn.RemoteAddr())
				response = []byte(fmt.Sprintf("$%d\r\n%s\r\n", len(value), value))
			} else {
				// Key does not exist, send a nil bulk string.
				slog.Info("GET executed", "key", key, "valueFound", false, "remoteAddr", msg.conn.RemoteAddr())
				response = []byte("$-1\r\n") // RESP nil bulk string.
			}
		}

	// PING or PING <message>
	// Responds with PONG or echoes back the message.
	case proto.CmdPing:
		// PING can have zero or one argument.
		if len(parsedCmd.Args) == 0 {
			// No arguments, respond with PONG.
			slog.Info("PING received (no args)", "remoteAddr", msg.conn.RemoteAddr())
			response = []byte("+PONG\r\n")
		} else if len(parsedCmd.Args) == 1 {
			// One argument, echo it back as a bulk string.
			message := parsedCmd.Args[0]
			slog.Info("PING received (with message)", "message", string(message), "remoteAddr", msg.conn.RemoteAddr())
			response = []byte(fmt.Sprintf("$%d\r\n%s\r\n", len(message), message))
		} else {
			// More than one argument is an error for PING.
			slog.Warn("PING command with wrong number of arguments", "argsReceived", len(parsedCmd.Args), "remoteAddr", msg.conn.RemoteAddr())
			response = []byte(fmt.Sprintf("-ERR wrong number of arguments for '%s' command\r\n", strings.ToLower(parsedCmd.CommandName)))
		}

	// QUIT
	// Sends +OK and closes the client connection.
	case proto.CmdQuit:
		slog.Info("QUIT received", "remoteAddr", msg.conn.RemoteAddr())
		response = []byte("+OK\r\n")         // Standard QUIT response.
		closeConnectionAfterResponse = true // Signal to close the connection.

	// DEL key [key ...]
	// Removes the specified keys. Returns the number of keys that were removed.
	case proto.CmdDel:
		// DEL command needs at least one key argument.
		if len(parsedCmd.Args) == 0 {
			slog.Warn("DEL command with no arguments", "remoteAddr", msg.conn.RemoteAddr())
			response = []byte(fmt.Sprintf("-ERR wrong number of arguments for '%s' command\r\n", strings.ToLower(parsedCmd.CommandName)))
		} else {
			deletedCount := 0 // Counter for how many keys were actually found and deleted.
			s.mu.Lock()       // Acquire write lock as we are modifying the data.
			for _, keyBytes := range parsedCmd.Args {
				key := string(keyBytes)
				// Check if the key exists before trying to delete.
				if _, ok := s.data[key]; ok {
					delete(s.data, key) // Delete the key.
					deletedCount++      // Increment our counter.
					slog.Debug("DEL executed for key", "key", key, "remoteAddr", msg.conn.RemoteAddr())
				}
			}
			s.mu.Unlock() // Release the write lock.

			slog.Info("DEL executed", "keysAttempted", len(parsedCmd.Args), "keysDeleted", deletedCount, "remoteAddr", msg.conn.RemoteAddr())
			response = []byte(fmt.Sprintf(":%d\r\n", deletedCount)) // RESP Integer reply.
		}

	// EXISTS key [key ...]
	// Returns the number of specified keys that exist.
	case proto.CmdExists:
		// EXISTS command needs at least one key argument.
		if len(parsedCmd.Args) == 0 {
			slog.Warn("EXISTS command with no arguments", "remoteAddr", msg.conn.RemoteAddr())
			response = []byte(fmt.Sprintf("-ERR wrong number of arguments for '%s' command\r\n", strings.ToLower(parsedCmd.CommandName)))
		} else {
			existingCount := 0 // Counter for how many of the provided keys exist.
			s.mu.RLock()       // Acquire read lock as we are only checking for existence.
			for _, keyBytes := range parsedCmd.Args {
				key := string(keyBytes)
				// Check if the key exists in our data store.
				if _, ok := s.data[key]; ok {
					existingCount++ // Increment if found.
					slog.Debug("EXISTS check for key", "key", key, "found", true, "remoteAddr", msg.conn.RemoteAddr())
				} else {
					slog.Debug("EXISTS check for key", "key", key, "found", false, "remoteAddr", msg.conn.RemoteAddr())
				}
			}
			s.mu.RUnlock() // Release the read lock.

			slog.Info("EXISTS executed", "keysChecked", len(parsedCmd.Args), "keysFound", existingCount, "remoteAddr", msg.conn.RemoteAddr())
			response = []byte(fmt.Sprintf(":%d\r\n", existingCount)) // RESP Integer reply.
		}

	// Unknown command
	// If parseCommand successfully parsed a command name but we don't recognize its type.
	case proto.CmdUnknown:
		slog.Warn("Unknown command received", "commandName", parsedCmd.CommandName, "remoteAddr", msg.conn.RemoteAddr())
		response = []byte(fmt.Sprintf("-ERR unknown command `%s`\r\n", SanitizeErrorMessage(parsedCmd.CommandName)))

	// Any unhandled command type
	// This case should ideally not be hit if parseCommand and the switch are comprehensive.
	default:
		slog.Error("Unhandled command type in switch", "commandType", parsedCmd.Type, "commandName", parsedCmd.CommandName, "remoteAddr", msg.conn.RemoteAddr())
		response = []byte(fmt.Sprintf("-ERR unhandled command type for '%s'\r\n", SanitizeErrorMessage(parsedCmd.CommandName)))
	}

	// If we have a response to send, write it back to the client.
	if response != nil {
		slog.Debug("Prepared response", "response", string(response), "remoteAddr", msg.conn.RemoteAddr())
		_, err := msg.conn.Write(response)
		if err != nil {
			// If writing fails, log it. The connection might be dead.
			slog.Error("Failed to write response to client", "err", err, "remoteAddr", msg.conn.RemoteAddr())
		}
	}

	// Close connection if QUIT was received and response was sent.
	if closeConnectionAfterResponse {
		slog.Info("Closing connection due to QUIT command", "remoteAddr", msg.conn.RemoteAddr())
		msg.conn.Close() // This will eventually lead to the peer being removed via delPeerCh.
	}
}

// loop is the main event loop for the server.
// It handles adding/removing peers, processing messages, and server shutdown.
func (s *Server) loop() {
	slog.Info("Server event loop started")
	defer slog.Info("Server event loop stopped") // This will log when the loop exits.

	for {
		select {
		// A new message from a client needs to be processed.
		case msg := <-s.msgCh:
			s.handleMessage(msg) // Delegate to the message handler.

		// A new client (peer) has connected.
		case peer := <-s.addPeerCh:
			slog.Info("Adding peer", "remoteAddr", peer.conn.RemoteAddr())
			s.peers[peer] = true // Register the peer.

		// A client (peer) has disconnected.
		case peer := <-s.delPeerCh:
			slog.Info("Deleting peer", "remoteAddr", peer.conn.RemoteAddr())
			delete(s.peers, peer) // Unregister the peer.

		// The server needs to shut down.
		case <-s.quitCh:
			slog.Info("Shutdown signal received in server loop. Closing listener and all peers.")
			// Close the main listener to stop accepting new connections.
			if s.ln != nil {
				s.ln.Close()
			}
			// Close all active peer connections.
			for p := range s.peers {
				slog.Info("Closing peer connection during shutdown", "remoteAddr", p.conn.RemoteAddr())
				p.conn.Close() // Closing the connection should trigger peer.readLoop to exit.
				delete(s.peers, p) // Clean up from the map.
			}
			return // Exit the loop, effectively stopping the server.
		}
	}
}

// acceptLoop continuously accepts new incoming TCP connections.
// Each new connection is then handled in its own goroutine.
func (s *Server) acceptLoop() {
	// This defer ensures that if acceptLoop exits (e.g., due to listener error),
	// the quit channel is closed to signal the rest of the server to shut down.
	defer func() {
		slog.Info("Accept loop shutting down.")
		select {
		case <-s.quitCh: // If already closed, do nothing.
		default:
			close(s.quitCh) // Ensure quitCh is closed.
		}
	}()

	for {
		// Block, waiting for a new connection.
		conn, err := s.ln.Accept()
		if err != nil {
			// If an error occurs, check if it's because the server is shutting down.
			select {
			case <-s.quitCh:
				slog.Info("Listener closed as part of server shutdown in acceptLoop.")
				return // Normal shutdown.
			default:
				// If not a shutdown, it's an unexpected error. Log and exit the loop.
				slog.Error("Accept error in acceptLoop", "err", err)
				// This return will trigger the deferred close(s.quitCh), signaling server shutdown.
				return
			}
		}
		// Successfully accepted a new connection.
		slog.Info("New connection accepted", "remoteAddr", conn.RemoteAddr())
		// Handle this new connection in a separate goroutine so we can accept more.
		go s.handleConn(conn)
	}
}

// handleConn sets up a new Peer for the connection and starts its read loop.
// It ensures the peer is cleaned up when the connection closes.
func (s *Server) handleConn(conn net.Conn) {
	// This defer ensures the connection is closed when handleConn exits.
	defer func() {
		slog.Info("Closing client connection in handleConn", "remoteAddr", conn.RemoteAddr())
		conn.Close()
	}()

	// Create a new Peer instance for this connection.
	// The peer will send messages to s.msgCh.
	peer := NewPeer(conn, s.msgCh)

	// Add the peer to the server's tracking.
	// Note: This was originally in s.loop(), but for immediate tracking it could be here.
	// However, to keep peer management centralized in s.loop(), we'll let peer.readLoop's exit trigger delPeerCh.
	// For adding, the original model of NewPeer -> addPeerCh <- peer; go peer.readLoop() is fine.
	// Let's assume Peer creation implies it will be added soon via addPeerCh if needed,
	// or that its lifecycle is managed by its readLoop sending to delPeerCh upon exit.
	// The current structure in the user's main.go has peer.readLoop() and then s.delPeerCh <- peer.
	// So, adding the peer to s.addPeerCh happens from where handleConn is called, or here before readLoop.
	// For now, I'll stick to the provided structure where peer management channels are used from the loops.
	// This specific handleConn doesn't add to addPeerCh, that's implicitly handled by how acceptLoop calls it
	// and how Peer interacts with msgCh and eventually delPeerCh.

	slog.Debug("Peer's readLoop starting", "remoteAddr", conn.RemoteAddr())
	// Start the peer's read loop. This will block until the connection
	// is closed or an error occurs in the loop.
	err := peer.readLoop() // Assuming peer.go has NewPeer and readLoop
	slog.Debug("Peer's readLoop finished", "remoteAddr", conn.RemoteAddr(), "error", err)

	// Once readLoop exits (connection closed, error, QUIT), signal the server
	// to remove this peer from its active peers list.
	s.delPeerCh <- peer
}

func main() {
	// Setting up structured logging as the default logger for the application.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil)) // Logs to Stderr by default.
	slog.SetDefault(logger)
	slog.Info("Starting Redis-go server...")

	// Create a new server instance with default configuration.
	server := NewServer(Config{})
	// Start the server. If Start() returns an error, it means the server
	// couldn't bind to the address or encountered another critical setup issue.
	if err := server.Start(); err != nil {
		slog.Error("Server startup failed", "err", err)
		log.Fatal(err) // log.Fatal will print the error and exit the program.
	}

	// The server is now running. We need to block the main goroutine until
	// the server signals it's quitting. This is done by reading from quitCh.
	// When quitCh is closed (by one of the server's shutdown paths), this read will unblock.
	<-server.quitCh
	slog.Info("Main: Server shutdown sequence complete. Exiting.")
	os.Exit(0) // Gracefully exit the application.
}

// You'll also need the Peer struct and its methods (NewPeer, readLoop)
// in a peer.go file (or in main.go if you prefer, though separate is cleaner).
// Make sure its readLoop sends raw command bytes to s.msgCh. For example:

/*
// Example of what might be in peer.go
package main // or appropriate package

import (
	"strings"	"bufio"
	"io"
	"log/slog"
	"net"
)

type Peer struct {
	conn  net.Conn
	msgCh chan<- Message // Use the Message struct defined in main.go
}

func NewPeer(conn net.Conn, msgCh chan<- Message) *Peer {
	return &Peer{
		conn:  conn,
		msgCh: msgCh,
	}
}

func (p *Peer) readLoop() error {
	// It's important that readLoop correctly reads full RESP commands.
	// Simple bufio.Reader().ReadBytes('\n') is NOT sufficient for RESP.
	// The proto.ParseCommand needs the raw bytes of one full command.
	// This often requires a more sophisticated reader that understands RESP arrays and bulk strings
	// to know how many bytes to read for a complete command.

	// For simplicity, if we assume each "message" from client is one full command block:
	// A more robust way would be to use a RESP parser directly here or buffer until a full command is formed.
	// The current model sends raw []byte from peer to server's msgCh,
	// and then server calls proto.ParseCommand. This is viable if peer.readLoop correctly frames commands.

	// Let's assume a simple line-based or fixed-buffer reading for now,
	// acknowledging that robust RESP framing is complex.
	// The provided proto.ParseCommand takes []byte, implying the framing happens before it.
	// So, this readLoop *must* send one complete RESP command as a []byte slice.

	// A common pattern for RESP is to read it in chunks or use a stateful parser.
	// For now, let's make a placeholder for how peer.readLoop might send data.
	// A proper implementation would involve correctly identifying command boundaries.

	reader := bufio.NewReader(p.conn)
	for {
		// This is a simplified way and might break with complex RESP.
		// A real RESP parser would need to understand types and lengths.
		// For example, reading the initial '*' for array, then lengths for bulk strings.
		// The current `proto.ParseCommand` expects a full command in `msg.data`.

		// Option 1: Try to read a "block" of data. This is non-trivial for RESP.
		// Let's simulate reading one command block, assuming it's available.
		// THIS IS A SIMPLIFICATION. A real RESP reader in the peer is needed.
		// For instance, you might read the type prefix, then length if applicable, then data.

		// For demonstration, let's assume the client sends commands one by one,
		// and we can read up to a certain buffer size or until a delimiter if using simplified protocol.
		// But since we are using RESP, `proto.ParseCommand` is designed for full RESP commands.
		// The peer needs to ensure it sends full commands.

		// A placeholder for robust command framing:
		// buf := make([]byte, 1024) // Example buffer
		// n, err := p.conn.Read(buf)
		// if err != nil {
		//    if err != io.EOF {
		//        slog.Error("Peer read error", "err", err, "remoteAddr", p.conn.RemoteAddr())
		//    }
		//    return err // EOF or other error
		// }
		// if n == 0 { continue } // Should not happen if err is nil
		// commandData := buf[:n]

		// For now, to make this runnable, let's imagine proto.ParseCommand handles partial reads
		// or that peer.readLoop gets one full command somehow.
		// The provided proto.ParseCommand uses bytes.Reader which implies it gets the whole command data at once.
		// This means handleConn or readLoop is responsible for framing.
		// This is a critical part. If peer.readLoop just reads arbitrary chunks, proto.ParseCommand will fail.

		// Let's assume `readFullRESPCommand` exists and is used by the peer:
		commandBytes, err := readFullRESPCommand(reader) // This function needs to be implemented robustly.
		if err != nil {
			if err != io.EOF { // Don't log EOF as an error necessarily, it's a clean disconnect.
				slog.Debug("Peer readLoop error or EOF", "err", err, "remoteAddr", p.conn.RemoteAddr())
			}
			return err // Propagate EOF or other errors to signal loop termination.
		}

		// Send the framed command data to the server's message channel.
		p.msgCh <- Message{
			conn: p.conn,
			data: commandBytes,
		}
	}
}

// readFullRESPCommand is a placeholder for a function that correctly reads
// one complete RESP command from the bufio.Reader.
// This is non-trivial and needs to parse RESP structure (arrays, bulk strings)
// to determine the end of a command.
// The current proto.ParseCommand expects the []byte passed to it to be one full command.
func readFullRESPCommand(r *bufio.Reader) ([]byte, error) {
	// Peek at the first byte to see the type.
	firstByte, err := r.Peek(1)
	if err != nil {
		return nil, err // Could be EOF or other error.
	}

	var cmdBuffer bytes.Buffer

	if firstByte[0] == '*' { // Array - standard for most commands
		// Read the array prefix line (e.g., *3\r\n)
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		cmdBuffer.Write(line) // Add "*N\r\n" to buffer

		// Parse N
		countStr := strings.TrimSpace(string(line[1:]))
		count, err := strconv.Atoi(countStr)
		if err != nil {
			return nil, fmt.Errorf("invalid array count: %s", countStr)
		}
		if count < 0 { // Null array is not a command, -1 is a valid count for other RESP types though.
			return nil, errors.New("invalid array count for command")
		}


		for i := 0; i < count; i++ {
			// Each element is typically a bulk string.
			// Read bulk string prefix (e.g., $5\r\n)
			bulkLenLine, err := r.ReadBytes('\n')
			if err != nil {
				return nil, err
			}
			cmdBuffer.Write(bulkLenLine) // Add "$M\r\n" to buffer

			lenStr := strings.TrimSpace(string(bulkLenLine[1:]))
			length, err := strconv.Atoi(lenStr)
			if err != nil {
				return nil, fmt.Errorf("invalid bulk string length: %s", lenStr)
			}

			if length == -1 { // Null bulk string
				// Nothing more to read for this element, it's already buffered.
			} else if length >= 0 {
				// Read the data itself + CRLF
				// Need length bytes + 2 bytes for CRLF
				dataAndCRLF := make([]byte, length+2)
				if _, err := io.ReadFull(r, dataAndCRLF); err != nil {
					return nil, err
				}
				cmdBuffer.Write(dataAndCRLF) // Add "data\r\n" to buffer
			} else {
				return nil, fmt.Errorf("invalid negative bulk string length: %d", length)
			}
		}
		return cmdBuffer.Bytes(), nil
	} else {
		// Inline command (e.g. PING\r\n) - less common from full clients but possible
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		cmdBuffer.Write(line)
		return cmdBuffer.Bytes(), nil
	}
}
*/
