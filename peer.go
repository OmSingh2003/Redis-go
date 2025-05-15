package main

import (
"fmt"
"net" // Imports the standard networking package
)
// Defines the structure representing a connected client (peer).
type Peer struct {
	// conn net.conn // Typo: Should be net.Conn (uppercase 'C')
	conn net.Conn   // Holds the network connection object for this specific peer.
}

// Constructor function for creating a new peer instance.
// func newpeer(conn new.conn) *peer { // Typo: Should be func newPeer(conn net.Conn) *Peer {
//                                   // And package should be 'net', not 'new'
func NewPeer(conn net.Conn) *Peer { // Renamed to newPeer for convention, corrected type
	return &Peer{ // Returns a pointer (*) to the newly created peer struct.
		conn: conn, // Initializes the conn field with the provided network connection.
	}
}

// Method associated with the peer struct to handle reading data from the client.
func (p *Peer) readLoop() error {
	buf := make([]byte ,1024)
	for {
		n,err :=p.conn.Read(buf)
		if err !=nil {
		return err
		}
		msgBuf := make([]byte n)
		copy(msgBuf ,buf[:n])
	 
	}

}
