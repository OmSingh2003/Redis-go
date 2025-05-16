package main

import (
	"fmt"
	"log"      // Added for log.Fatal
	"log/slog" // For structured logging
	"net"
	"sync" // <<<< ADD THIS IMPORT for sync.RWMutex
)

// defaultListenAdrr is used if ListenAddress is not specified.
const defaultListenAdrr = ":5005" // Changed port slightly for common alternative, was :3333

// Config holds the server's configuration.
type Config struct {
	ListenAddress string
}

// Server manages peers, listens for incoming connections, and stores data.
type Server struct {
	Config    // Embed the Config struct
	peers     map[*Peer]bool
	ln        net.Listener
	addPeerCh chan *Peer
	quitCh    chan struct{}
	msgCh     chan []byte

	// --- In-Memory Store ---
	mu   sync.RWMutex          // Mutex to protect concurrent access to 'data'
	data map[string][]byte     // The actual key-value store
	// -----------------------
}

// NewServer creates and returns a new Server instance.
func NewServer(cfg Config) *Server {
	// If ListenAddress is not specified, use the default.
	if cfg.ListenAddress == "" { // Check if empty, not len(cfg.ListenAddress) == 0 for clarity
		cfg.ListenAddress = defaultListenAdrr
	}

	return &Server{
		Config:    cfg,
		peers:     make(map[*Peer]bool),
		addPeerCh: make(chan *Peer),
		quitCh:    make(chan struct{}), // Consistent with struct definition
		msgCh:     make(chan []byte),
		// --- Initialize the data store ---
		data: make(map[string][]byte),
		// mu is zero-valued and ready to use
		// --------------------------------
	}
}

// Start initializes the server and begins listening for connections.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.ListenAddress)
	if err != nil {
		return err
	}
	s.ln = ln

	go s.loop()

	slog.Info("Server running", "ListenAddress", s.ListenAddress)

	go s.acceptLoop()

	slog.Info("Server started", "listening_on", s.ListenAddress)
	return nil
}

// handleRawMessage will eventually parse and execute commands.
// For now, it just prints. Later it will use s.mu and s.data.
func (s *Server) handleRawMessage(rawMsg []byte) error {
	// TODO: Later, parse command here. For a SET command:
	// s.mu.Lock()
	// s.data["someKey"] = []byte("someValue")
	// s.mu.Unlock()
	// For a GET command:
	// s.mu.RLock()
	// value := s.data["someKey"]
	// s.mu.RUnlock()
	// process value...

	slog.Info("Received raw message", "data", string(rawMsg)) // Changed fmt.Println to slog.Info
	return nil
}

// loop is the main event loop for the server, handling peer additions and quit signals.
func (s *Server) loop() {
	for {
		select {
		case rawMsg := <-s.msgCh:
			// Note: We'll need to pass the peer or its connection
			// to handleRawMessage if it needs to send a response.
			// For now, handleRawMessage only interacts with s.data
			if err := s.handleRawMessage(rawMsg); err != nil {
				slog.Error("raw message error", "err", err)
			}
		case <-s.quitCh:
			slog.Info("Server loop shutting down.")
			// TODO: Gracefully disconnect peers if necessary
			return
		case peer := <-s.addPeerCh:
			slog.Info("Peer connected", "remote_addr", peer.conn.RemoteAddr().String())
			s.peers[peer] = true
		}
	}
}

// acceptLoop continuously accepts new incoming connections.
func (s *Server) acceptLoop() {
	defer func() {
		slog.Info("Accept loop shutting down.")
	}()

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if opErr, ok := err.(*net.OpError); ok && opErr.Op == "accept" {
				slog.Warn("Accept loop: Listener closed, stopping.", "error", err.Error())
				// If the listener is closed, it's usually a signal to shut down the server.
				// Consider closing s.quitCh here if not handled elsewhere,
				// to ensure the main loop also terminates.
				// close(s.quitCh) // Example: This would trigger server shutdown
				return
			}
			slog.Error("Accept error", "err", err)
			continue
		}
		go s.handleConn(conn)
	}
}

// handleConn handles a new connection by creating a peer and starting its read loop.
func (s *Server) handleConn(conn net.Conn) {
	peer := NewPeer(conn, s.msgCh) // Pass s.msgCh to the peer
	s.addPeerCh <- peer            // Send the new peer to the main loop
	slog.Info("new peer connected", "remoteAddr", conn.RemoteAddr())

	// The readLoop will handle communication with this peer.
	if err := peer.readLoop(); err != nil {
		slog.Error("peer read error", "err", err, "remoteAddr", conn.RemoteAddr())
	}

	// Optional: Logic to remove peer from s.peers when readLoop finishes.
	// This would typically involve sending the peer to a removePeerCh and handling it in s.loop().
	slog.Info("Peer disconnected", "remote_addr", conn.RemoteAddr().String())
	// delete(s.peers, peer) // This needs to be done in the main 'loop' goroutine or synchronized.
						  // For simplicity, we'll omit detailed peer removal for now.
						  // A more robust implementation would handle this in the s.loop select.
}

func main() {
	server := NewServer(Config{})
	if err := server.Start(); err != nil {
		log.Fatal(err)
	}

	// Wait for the server to signal shutdown.
	// This happens if acceptLoop exits (e.g. listener closed) and closes quitCh,
	// or if another part of the application signals a shutdown.
	<-server.quitCh
	slog.Info("Main: Server shutdown initiated.")
	// Add any other cleanup logic here if needed before full exit.
}
