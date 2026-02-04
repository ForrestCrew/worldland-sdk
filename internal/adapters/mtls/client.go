package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"net"
)

// Command represents a command received from Hub
type Command struct {
	ID      string                 `json:"id"`
	Type    string                 `json:"type"`
	Payload map[string]interface{} `json:"payload"`
}

// CommandAck represents acknowledgment sent to Hub
type CommandAck struct {
	CommandID string                 `json:"command_id"`
	Status    string                 `json:"status"` // "ok" or "error"
	Error     string                 `json:"error,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"` // Additional response data
}

// Client handles mTLS connection to Hub
type Client struct {
	hubAddr  string
	cert     tls.Certificate
	rootCAs  *x509.CertPool
	conn     net.Conn
	stopCh   chan struct{}

	// OnCommand is called when a command is received from Hub
	OnCommand func(cmd Command) CommandAck
}

// NewClient creates a new mTLS client
func NewClient(hubAddr string, cert tls.Certificate, rootCAs *x509.CertPool) *Client {
	return &Client{
		hubAddr: hubAddr,
		cert:    cert,
		rootCAs: rootCAs,
		stopCh:  make(chan struct{}),
	}
}

// Connect establishes mTLS connection to Hub
func (c *Client) Connect() error {
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{c.cert},
		RootCAs:      c.rootCAs,
		MinVersion:   tls.VersionTLS13, // TLS 1.3 only per research
		MaxVersion:   tls.VersionTLS13,
	}

	conn, err := tls.Dial("tcp", c.hubAddr, tlsConfig)
	if err != nil {
		return err
	}

	// Explicitly perform handshake to catch TLS errors early
	if err := conn.Handshake(); err != nil {
		conn.Close()
		return fmt.Errorf("TLS handshake failed: %w", err)
	}

	c.conn = conn
	log.Printf("Connected to Hub via mTLS")
	return nil
}

// Listen starts listening for commands from Hub
func (c *Client) Listen() {
	defer c.conn.Close()

	buf := make([]byte, 4096)
	for {
		select {
		case <-c.stopCh:
			return
		default:
			n, err := c.conn.Read(buf)
			if err != nil {
				log.Printf("Read error: %v", err)
				return
			}

			var cmd Command
			if err := json.Unmarshal(buf[:n], &cmd); err != nil {
				log.Printf("Failed to parse command: %v", err)
				continue
			}

			// Handle command
			var ack CommandAck
			if c.OnCommand != nil {
				ack = c.OnCommand(cmd)
			} else {
				ack = CommandAck{CommandID: cmd.ID, Status: "ok"}
			}

			// Send acknowledgment
			ackData, _ := json.Marshal(ack)
			if _, err := c.conn.Write(ackData); err != nil {
				log.Printf("Failed to send ack: %v", err)
			}
		}
	}
}

// Close closes the connection
func (c *Client) Close() {
	close(c.stopCh)
	if c.conn != nil {
		c.conn.Close()
	}
}
