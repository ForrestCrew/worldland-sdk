package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
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
	connMu   sync.Mutex
	stopCh   chan struct{}

	// OnCommand is called when a command is received from Hub
	OnCommand func(cmd Command) CommandAck

	// OnReconnected is called after a successful reconnection
	OnReconnected func()
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

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	log.Printf("Connected to Hub via mTLS")
	return nil
}

// Listen starts listening for commands from Hub with automatic reconnection
func (c *Client) Listen() {
	for {
		c.listenOnce()

		// Check if we should stop
		select {
		case <-c.stopCh:
			return
		default:
		}

		// Reconnect with backoff
		log.Printf("Connection to Hub lost, reconnecting...")
		backoff := 5 * time.Second
		maxBackoff := 60 * time.Second
		for {
			select {
			case <-c.stopCh:
				return
			case <-time.After(backoff):
			}

			if err := c.Connect(); err != nil {
				log.Printf("Reconnect failed: %v (retry in %v)", err, backoff)
				backoff = backoff * 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}

			log.Printf("Reconnected to Hub")
			if c.OnReconnected != nil {
				c.OnReconnected()
			}
			break
		}
	}
}

// listenOnce reads from current connection until error
func (c *Client) listenOnce() {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn == nil {
		return
	}

	defer conn.Close()

	buf := make([]byte, 4096)
	for {
		select {
		case <-c.stopCh:
			return
		default:
			n, err := conn.Read(buf)
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
			if _, err := conn.Write(ackData); err != nil {
				log.Printf("Failed to send ack: %v", err)
			}
		}
	}
}

// Send sends a raw message to Hub via mTLS connection
func (c *Client) Send(data []byte) error {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}
	_, err := conn.Write(data)
	return err
}

// Close closes the connection
func (c *Client) Close() {
	close(c.stopCh)
	c.connMu.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.connMu.Unlock()
}
