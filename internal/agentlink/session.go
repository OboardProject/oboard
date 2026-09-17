package agentlink

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
)

// Session is an established transport connection. It multiplexes the message
// channel (the WS-equivalent stream), RPC-style requests, and data streams.
type Session struct {
	conn      net.Conn
	certPin   string
	writeMu   sync.Mutex
	nextReqID int64
	pendingMu sync.Mutex
	pending   map[int64]chan *ResponseFrame

	// acceptPayload carries the auth-acceptance payload (the enrollment
	// response for a bootstrapping session) for the caller to read once.
	acceptPayload []byte
	msgHandler    func(map[string]json.RawMessage)
	dataHandlerMu sync.Mutex
	dataHandler   func(DataHeader, []byte) error
	closed        chan struct{}
	closeOnce     sync.Once
	writeErr      error
	writeErrMu    sync.Mutex
}

// ErrClosed is returned once the session has ended.
var ErrClosed = errors.New("agentlink session closed")

// WriteMessage sends one JSON message envelope, padded to the minimum frame
// size.
func (s *Session) WriteMessage(envelope any) error {
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(payload) > MaxMessageBytes {
		return ErrMessageTooLarge
	}
	return s.writePadded(frame{frameType: frameTypeMessage, flags: flagPayloadJSON}, payload)
}

// Request performs one RPC round-trip over the session.
func (s *Session) Request(ctx context.Context, path string, body any, out any) (*ResponseFrame, error) {
	s.pendingMu.Lock()
	s.nextReqID++
	id := s.nextReqID
	ch := make(chan *ResponseFrame, 1)
	s.pending[id] = ch
	s.pendingMu.Unlock()
	var raw json.RawMessage
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			s.pendingMu.Lock()
			delete(s.pending, id)
			s.pendingMu.Unlock()
			return nil, err
		}
		raw = encoded
	}
	req := RequestFrame{ID: id, Path: path, Body: raw}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := s.writePadded(frame{frameType: frameTypeRequest, flags: flagPayloadJSON}, payload); err != nil {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if out != nil && len(resp.Body) > 0 {
			if err := json.Unmarshal(resp.Body, out); err != nil {
				return resp, err
			}
		}
		return resp, nil
	case <-s.closed:
		return nil, ErrClosed
	}
}

// WriteFrame sends a raw frame.
func (s *Session) WriteFrame(f frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.writeErrValue(); err != nil {
		return err
	}
	if err := writeFrame(s.conn, f); err != nil {
		s.setWriteErr(err)
		return err
	}
	return nil
}

// writePadded sends a frame whose payload is padded to the minimum size.
func (s *Session) writePadded(f frame, payload []byte) error {
	if len(payload)+frameHeaderLen >= MinPaddedFrameBytes {
		f.payload = payload
		return s.WriteFrame(f)
	}
	padLen := MinPaddedFrameBytes - frameHeaderLen - len(payload)
	f.flags |= flagPad
	padded, err := buildPadding(payload, padLen)
	if err != nil {
		return err
	}
	f.payload = padded
	return s.WriteFrame(f)
}

func (s *Session) writeErrValue() error {
	s.writeErrMu.Lock()
	defer s.writeErrMu.Unlock()
	return s.writeErr
}

func (s *Session) setWriteErr(err error) {
	s.writeErrMu.Lock()
	s.writeErr = err
	s.writeErrMu.Unlock()
}

func (s *Session) shutdown(err error) {
	s.closeOnce.Do(func() {
		s.setWriteErr(err)
		close(s.closed)
		_ = s.conn.Close()
		s.pendingMu.Lock()
		for id, ch := range s.pending {
			close(ch)
			delete(s.pending, id)
		}
		s.pendingMu.Unlock()
	})
}

// RunData starts a data-frame read loop alongside the message loop. Only one
// RunData may be active per session.
func (s *Session) RunData(onData func(DataHeader, []byte) error) error {
	s.dataHandlerMu.Lock()
	if s.dataHandler != nil {
		s.dataHandlerMu.Unlock()
		return errors.New("agentlink data handler already active")
	}
	s.dataHandler = onData
	s.dataHandlerMu.Unlock()
	defer func() {
		s.dataHandlerMu.Lock()
		s.dataHandler = nil
		s.dataHandlerMu.Unlock()
	}()
	<-s.Closed()
	return nil
}

// deliverData routes one data frame to the active handler.
func (s *Session) deliverData(f frame) error {
	s.dataHandlerMu.Lock()
	handler := s.dataHandler
	s.dataHandlerMu.Unlock()
	if handler == nil {
		return nil
	}
	if len(f.payload) < 2 {
		return nil
	}
	headerLen := int(f.payload[0])<<8 | int(f.payload[1])
	if 2+headerLen > len(f.payload) {
		return nil
	}
	var header DataHeader
	if err := json.Unmarshal(f.payload[2:2+headerLen], &header); err != nil {
		return err
	}
	return handler(header, f.payload[2+headerLen:])
}

// Close ends the session.
func (s *Session) Close() error {
	s.shutdown(ErrClosed)
	return nil
}

// Closed reports whether the session has ended.
func (s *Session) Closed() <-chan struct{} { return s.closed }

// Ping sends one keepalive frame.
func (s *Session) Ping() error {
	return s.WriteFrame(frame{frameType: frameTypePing})
}

