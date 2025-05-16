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
// default port where my  redis server would wait for TCP conections from clients
const defaultListenAdrr = ":5005" //Deafult listening Address (if client does not specify the port this will be used )

type Config struct { // config structure
	ListenAddress string  //store address and port 
}

type Message struct {
	conn net.Conn //network connection (client)
	data []byte   //redis cmd 
}

type Server struct {
	Config    // inherits ListenAddress
	peers     map[*Peer]bool //keep track of connected clients
	ln        net.Listener //TCP listener returned by net.Listen
	addPeerCh chan *Peer // Channel for adding peers
	delPeerCh chan *Peer  // channel for deleting peers
	quitCh    chan struct{} // signal to shutdown the server gracefully
	msgCh     chan Message //channel were iconimg messages structs are sent.

// preventing race condtions.
	mu   sync.RWMutex // prevents access to the in-memory data 
	data map[string][]byte // key value store in (Redis in-memory database)
}

func NewServer(cfg Config) *Server { //intializing server instance
	//if no ListenAddress is stated use the default one 
	if cfg.ListenAddress == "" { 
		cfg.ListenAddress = defaultListenAdrr
	}
//intializing 
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

	go s.loop() // gouroutine for the main server loop

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


func (s *Server) handleMessage(msg Message) {
	parsedCmd, err := parseCommand(msg.data) // Command Parsing 
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
		"argsCount", len(parsedCmd.Args),
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

	case CmdPing: 
		if len(parsedCmd.Args) == 0 {
			// PING (no arguments)
			slog.Info("PING received (no args)", "remoteAddr", msg.conn.RemoteAddr())
			response = []byte("+PONG\r\n")
		} else if len(parsedCmd.Args) == 1 {
			// PING <message>
			message := parsedCmd.Args[0]
			slog.Info("PING received (with message)", "message", message, "remoteAddr", msg.conn.RemoteAddr())
			response = []byte(fmt.Sprintf("$%d\r\n%s\r\n", len(message), message))
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
