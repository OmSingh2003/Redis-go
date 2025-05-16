package main

import (
	"io"
	"net"
	"log/slog"
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
	buf := make([]byte, 2048) 
	for {
		n, err := p.conn.Read(buf)
		if err != nil {
			if err == io.EOF {
				slog.Info("Client closed connection (EOF)", "remoteAddr", p.conn.RemoteAddr())
			} else {
				slog.Error("Error reading from peer connection", "err", err, "remoteAddr", p.conn.RemoteAddr())
			}
			return err
		}

		msgData := make([]byte, n)
		copy(msgData, buf[:n])

		p.msgCh <- Message{
			conn: p.conn,
			data: msgData, 
		}
	}
}
