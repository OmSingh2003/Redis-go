package main

import (
	"log"      // Added for log.Fatal
	"net"
	"log/slog" // For structured logging
)

// defaultListenAdrr is used if ListenAddress is not specified.
const defaultListenAdrr = ":3000" // Changed port slightly for common alternative, was :3333

// Config holds the server's configuration.
type Config struct {
	ListenAddress string
}

// Server manages peers and listens for incoming connections.
type Server struct {
	Config // Embed the Config struct
	peers     map[*Peer]bool
	ln        net.Listener
	addPeerCh chan *Peer
	quitCh    chan struct{}
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

	slog.Info("Server running","ListenAddress",s.ListenAddress)

	go s.acceptLoop()

	slog.Info("Server started", "listening_on", s.ListenAddress)
	return nil
}

// loop is the main event loop for the server, handling peer additions and quit signals.
func (s *Server) loop() {
	for {
		select {
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
		// If acceptLoop exits (e.g., due to listener close), signal the main loop to quit.
		// This might not be strictly necessary if s.ln.Close() is handled elsewhere before quitCh.
		// close(s.quitCh) // Consider the shutdown sequence carefully.
	}()

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			// If the error is due to the listener being closed, it might be a planned shutdown.
			// Otherwise, it's an actual error.
			if opErr, ok := err.(*net.OpError); ok && opErr.Op == "accept" {
				slog.Warn("Accept loop: Listener closed, stopping.", "error", err.Error())
				return // Exit loop if listener is closed
			}
			slog.Error("Accept error", "err", err)
			continue
		}
		go s.handleConn(conn)
	}
}

// handleConn handles a new connection by creating a peer and starting its read loop.
func (s *Server) handleConn(conn net.Conn) {
	peer := NewPeer(conn) // Uses NewPeer from peer.go
	s.addPeerCh <- peer   // Send the new peer to the main loop
	slog.Info ("new peer  connected ","remoteAddr",conn.RemoteAddr())
	// The readLoop will handle communication with this peer.
	// If readLoop exits, it should handle cleanup for that peer (e.g., closing conn).
	if err:=	peer.readLoop();err!=nil {
		slog.Error("peer read error" , "err" ,err ,"remoteAddr", conn.RemoteAddr())
	}

	// Optional: Logic to remove peer from s.peers when readLoop finishes.
	// This would require peer.readLoop to signal its completion or for
	// the loop to periodically check peer status.
	// For now, we'll keep it simple.
	slog.Info("Peer disconnected", "remote_addr", conn.RemoteAddr().String())
	// delete(s.peers, peer) // This needs to be done in the main 'loop' goroutine or synchronized.
}

func main() {
	// Pass Config struct literal.
	// You can customize the ListenAddress here if needed:
	// server := NewServer(Config{ListenAddress: ":5000"})
	server := NewServer(Config{})
	if err := server.Start(); err != nil {
		log.Fatal(err) // Use log.Fatal for startup errors
	}

	// Keep the main goroutine alive (e.g., wait for a signal to shut down)
	// For a real server, you'd handle signals like SIGINT, SIGTERM here.
	// For this example, it will run until manually stopped or an unrecoverable error.
	// A simple way to keep it running if Start() doesn't block:
	// select {}
	// However, our server.Start() returns nil, so the program would exit.
	// The goroutines for loop and acceptLoop will keep it alive.
	// For a graceful shutdown, you would signal s.quitCh from here based on OS signals.
	// For now, it runs until the acceptLoop or main loop terminates due to an issue.
	// If using `log.Fatal`, the program exits if server.Start() returns an error.
	// If server.Start() is successful, the server runs in goroutines.
	// To make it wait, you might need a blocking call or a signal handling mechanism.
	// For simplicity, if Start is successful, the goroutines will run.
	// If acceptLoop errors out in a way that makes it exit, quitCh should be closed.
	<-server.quitCh // Wait for the server to signal shutdown (e.g. after acceptLoop error)
	slog.Info("Main: Server shutdown complete.")
}
