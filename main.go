package main 
import (
	"net"
)
const defaultListenAdrr = ":3333"
type Config struct {
	ListenAddress string
}
type Server struct {
	Config
	ln net.Listener
}
func NewServer(cfg Config ) *Server{
	if len(cfg.ListenAddress) == 0 {
		cfg :=defaultListenAdrr

	}
	return &Server{
		Config : cfg,
	}
}
func (s *Server) Start() error{
	ln,err :=net.Listen("tcp",s.ListenAddress)
	if err!=nil {
		retunr err
	}
	s.ln = ln 
	go s.acceptLoop()
}
func (s *Server) acceptLoop () {
 for {
		conn ,err :=s.ln.Accept()
		if err !=nil{
			slog.Error("accept error" , "err" , err)
			continue
		}
		go s.handleConn(conn)
	}
}
func (s *Server) handleConn(conn net.Conn){

}
func main() {

}
