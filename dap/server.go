package dap

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
)

// BreakpointManagerInterface defines the methods the DAP server needs from the breakpoint manager.
type BreakpointManagerInterface interface {
	SetBreakpoints(file string, lines []int)
	IsSet(file string, line int) bool
	HasAnyBreakpoints() bool
	Resume(vuID uint64, action int)
	ResumeAll(action int)
	ListVUs() []uint64
	GetVUVariables(vuID uint64) map[string]any
	GetVUPauseLocation(vuID uint64) (file string, line int, paused bool)
	SetOnStopped(fn func(vuID uint64, file string, line int))
	SetOnThread(fn func(vuID uint64, reason string))
}

// Server is a DAP (Debug Adapter Protocol) TCP server.
// It accepts a single IDE connection and handles DAP messages.
type Server struct {
	addr    string
	handler *Handler
	conn    net.Conn
	mu      sync.Mutex
	writer  *bufio.Writer
	seq     int
	closed  bool

	// ready is closed when the client has finished configuration (configurationDone).
	ready chan struct{}
}

// NewServer creates a new DAP server.
func NewServer(addr string, handler *Handler) *Server {
	s := &Server{
		addr:    addr,
		handler: handler,
		ready:   make(chan struct{}),
	}
	handler.server = s
	return s
}

// WaitForClient blocks until a DAP client has connected and sent configurationDone.
// This prevents VUs from running before the IDE has a chance to set breakpoints.
func (s *Server) WaitForClient() {
	<-s.ready
}

// SignalReady is called when the client has finished configuration.
func (s *Server) SignalReady() {
	select {
	case <-s.ready:
		// already closed
	default:
		close(s.ready)
	}
}

// Start listens for a single DAP client connection and handles messages.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("DAP server listen error: %w", err)
	}
	defer ln.Close()

	log.Printf("[k6-debug] DAP server listening on %s", s.addr)

	conn, err := ln.Accept()
	if err != nil {
		return fmt.Errorf("DAP server accept error: %w", err)
	}
	log.Printf("[k6-debug] DAP client connected from %s", conn.RemoteAddr())

	s.mu.Lock()
	s.conn = conn
	s.writer = bufio.NewWriter(conn)
	s.mu.Unlock()

	s.handleConnection(conn)
	return nil
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

	for {
		msg, err := readMessage(reader)
		if err != nil {
			if err == io.EOF {
				log.Printf("[k6-debug] DAP client disconnected")
			} else {
				log.Printf("[k6-debug] DAP read error: %v", err)
			}
			return
		}

		log.Printf("[k6-debug] DAP recv: type=%s command=%s seq=%d", msg.Type, msg.Command, msg.Seq)

		response := s.handler.HandleMessage(msg)
		if response != nil {
			for _, resp := range response {
				if err := s.sendMessage(resp); err != nil {
					log.Printf("[k6-debug] DAP write error: %v", err)
					return
				}
			}
		}
	}
}

// SendEvent sends an asynchronous DAP event to the connected IDE.
func (s *Server) SendEvent(event *Message) error {
	return s.sendMessage(event)
}

func (s *Server) sendMessage(msg *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn == nil || s.closed {
		return fmt.Errorf("no DAP connection")
	}

	s.seq++
	msg.Seq = s.seq

	data := marshalMessage(msg)
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := s.writer.WriteString(header); err != nil {
		return err
	}
	if _, err := s.writer.Write(data); err != nil {
		return err
	}
	return s.writer.Flush()
}

// Close shuts down the DAP server connection.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.conn != nil {
		s.conn.Close()
	}
}
