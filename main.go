package main

import (
	"bytes"
	"fmt"
	"log"      
	"log/slog" 
	"net"
	"os" 
	"sync"
)

const defaultListenAdrr = ":5005"

type Config struct {
	ListenAddress string
}

type Message struct {
	conn net.Conn 
	data []byte   
}

type Server struct {
	Config    
	peers     map[*Peer]bool
	ln        net.Listener
	addPeerCh chan *Peer
	delPeerCh chan *Peer 
	quitCh    chan struct{}
	msgCh     chan Message

	mu   sync.RWMutex
	data map[string][]byte
}

func NewServer(cfg Config) *Server {
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = defaultListenAdrr
	}

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

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.ListenAddress)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.ListenAddress, err)
	}
	s.ln = ln

	go s.loop()

	slog.Info("Server running", "ListenAddress", s.ListenAddress)
	slog.Info("Accepting connections...")
	go s.acceptLoop()

	return nil
}

func SanitizeErrorMessage(msg string) string {
	var sanitized bytes.Buffer
	for _, r := range msg {
		if r != '\r' && r != '\n' {
			sanitized.WriteRune(r)
		}
	}
	return sanitized.String()
}


func (s *Server) handleMessage(msg Message) {
	parsedCmd, err := parseCommand(msg.data)
	if err != nil {
		slog.Error("Failed to parse command", "err", err, "remoteAddr", msg.conn.RemoteAddr(), "rawData", string(msg.data))
		errorResponse := []byte(fmt.Sprintf("-ERR %s\r\n", SanitizeErrorMessage(err.Error())))
		_, writeErr := msg.conn.Write(errorResponse)
		if writeErr != nil {
			slog.Error("Failed to write error response to client", "err", writeErr, "remoteAddr", msg.conn.RemoteAddr())
		}
		return
	}

	slog.Debug("Executing command",
		"commandName", parsedCmd.CommandName,
		"key", parsedCmd.Key,

		"remoteAddr", msg.conn.RemoteAddr())

	var response []byte

	switch parsedCmd.Type {
	case CmdSet:
		s.mu.Lock()
		s.data[parsedCmd.Key] = []byte(parsedCmd.Val)
		s.mu.Unlock()
		slog.Info("SET executed", "key", parsedCmd.Key, /* "value", parsedCmd.Val, */ "remoteAddr", msg.conn.RemoteAddr())
		response = []byte("+OK\r\n")

	case CmdGet:
		s.mu.RLock()
		value, ok := s.data[parsedCmd.Key]
		s.mu.RUnlock()

		if ok {
			slog.Info("GET executed", "key", parsedCmd.Key, "valueFound", true, "remoteAddr", msg.conn.RemoteAddr())
			response = []byte(fmt.Sprintf("$%d\r\n%s\r\n", len(value), value))
		} else {
			slog.Info("GET executed", "key", parsedCmd.Key, "valueFound", false, "remoteAddr", msg.conn.RemoteAddr())
			response = []byte("$-1\r\n")
		}
	case CmdUnknown:
		
		slog.Warn("Unknown command received", "commandName", parsedCmd.CommandName, "remoteAddr", msg.conn.RemoteAddr())
		response = []byte(fmt.Sprintf("-ERR unknown command `%s`\r\n", SanitizeErrorMessage(parsedCmd.CommandName)))

	default:
		slog.Error("Unhandled command type in switch", "commandType", parsedCmd.Type, "commandName", parsedCmd.CommandName, "remoteAddr", msg.conn.RemoteAddr())
		response = []byte(fmt.Sprintf("-ERR unhandled command type for '%s'\r\n", SanitizeErrorMessage(parsedCmd.CommandName)))
	}

	if response != nil {
		slog.Debug("Prepared response", "response", string(response), "remoteAddr", msg.conn.RemoteAddr())
		_, err := msg.conn.Write(response)
		if err != nil {
			slog.Error("Failed to write response to client", "err", err, "remoteAddr", msg.conn.RemoteAddr())
		}
	}
}

func (s *Server) loop() {
	slog.Info("Server event loop started")
	defer slog.Info("Server event loop stopped")

	for {
		select {
		case msg := <-s.msgCh:
			s.handleMessage(msg)

		case peer := <-s.addPeerCh:
			slog.Info("Adding peer", "remoteAddr", peer.conn.RemoteAddr())
			s.peers[peer] = true

		case peer := <-s.delPeerCh:
			slog.Info("Deleting peer", "remoteAddr", peer.conn.RemoteAddr())
			delete(s.peers, peer)

		case <-s.quitCh:
			slog.Info("Shutdown signal received in server loop. Closing listener and all peers.")
			if s.ln != nil {
				s.ln.Close() 
			}
			for p := range s.peers {
				slog.Info("Closing peer connection during shutdown", "remoteAddr", p.conn.RemoteAddr())
				p.conn.Close()
				delete(s.peers, p)
			}
			return //
		}
	}
}

func (s *Server) acceptLoop() {
	defer func() {
		slog.Info("Accept loop shutting down.")
		select {
		case <-s.quitCh:
		default:
			close(s.quitCh)
		}
	}()

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.quitCh: 
				slog.Info("Listener closed as part of server shutdown in acceptLoop.")
				return
			default:
				slog.Error("Accept error in acceptLoop", "err", err)
				return
			}
		}
		slog.Info("New connection accepted", "remoteAddr", conn.RemoteAddr())
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer func() {
		slog.Info("Closing client connection in handleConn", "remoteAddr", conn.RemoteAddr())
		conn.Close()
	}()

	peer := NewPeer(conn, s.msgCh)

	slog.Debug("Peer's readLoop starting", "remoteAddr", conn.RemoteAddr())
	err := peer.readLoop()
	slog.Debug("Peer's readLoop finished", "remoteAddr", conn.RemoteAddr(), "error", err)

	s.delPeerCh <- peer
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
slog.SetDefault(logger)
	slog.Info("Starting Redis-go server...")
	server := NewServer(Config{})
	if err := server.Start(); err != nil {
		slog.Error("Server startup failed", "err", err)
		log.Fatal(err) 
	}

	<-server.quitCh
	slog.Info("Main: Server shutdown sequence complete. Exiting.")
	os.Exit(0) 
}
