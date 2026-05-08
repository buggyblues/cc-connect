package shadowob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type socketEvent struct {
	Name string
	Data json.RawMessage
}

type socketClient struct {
	serverURL string
	token     string

	conn    *websocket.Conn
	writeMu sync.Mutex

	ackMu    sync.Mutex
	nextAck  int
	ackChans map[int]chan json.RawMessage
}

func newSocketClient(serverURL, token string) *socketClient {
	return &socketClient{
		serverURL: normalizeServerURL(serverURL),
		token:     token,
		ackChans:  make(map[int]chan json.RawMessage),
	}
}

func (s *socketClient) connect(ctx context.Context) error {
	wsURL, err := socketURL(s.serverURL)
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, http.Header{})
	if err != nil {
		return fmt.Errorf("shadowob: websocket dial: %w", err)
	}
	s.conn = conn
	return nil
}

func (s *socketClient) close() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.ackMu.Lock()
	for id, ch := range s.ackChans {
		close(ch)
		delete(s.ackChans, id)
	}
	s.ackMu.Unlock()
}

func socketURL(serverURL string) (string, error) {
	u, err := url.Parse(normalizeServerURL(serverURL))
	if err != nil {
		return "", fmt.Errorf("shadowob: parse server_url: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("shadowob: unsupported server_url scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/socket.io/"
	q := u.Query()
	q.Set("EIO", "4")
	q.Set("transport", "websocket")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *socketClient) readLoop(ctx context.Context, onConnect func(), onEvent func(socketEvent)) error {
	defer s.close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("shadowob: websocket read: %w", err)
		}
		for _, packet := range strings.Split(string(data), "\x1e") {
			if packet == "" {
				continue
			}
			switch packet[0] {
			case '0':
				if err := s.writePacket(socketConnectPacket(s.token)); err != nil {
					return err
				}
			case '2':
				if err := s.writePacket("3"); err != nil {
					return err
				}
			case '3':
				continue
			case '4':
				if err := s.handleSocketPacket(packet, onConnect, onEvent); err != nil {
					slog.Debug("shadowob: ignored socket packet", "packet", truncateLog(packet, 300), "error", err)
				}
			default:
				slog.Debug("shadowob: ignored engine packet", "packet", truncateLog(packet, 300))
			}
		}
	}
}

func socketConnectPacket(token string) string {
	data, _ := json.Marshal(map[string]string{"token": token})
	return "40" + string(data)
}

func (s *socketClient) handleSocketPacket(packet string, onConnect func(), onEvent func(socketEvent)) error {
	if packet == "40" || strings.HasPrefix(packet, "40{") {
		if onConnect != nil {
			onConnect()
		}
		return nil
	}
	if strings.HasPrefix(packet, "44") {
		return fmt.Errorf("socket.io error: %s", strings.TrimPrefix(packet, "44"))
	}
	if strings.HasPrefix(packet, "42") {
		ev, err := parseSocketEvent(packet)
		if err != nil {
			return err
		}
		if onEvent != nil {
			onEvent(ev)
		}
		return nil
	}
	if strings.HasPrefix(packet, "43") {
		id, payload, err := parseSocketAck(packet)
		if err != nil {
			return err
		}
		s.deliverAck(id, payload)
		return nil
	}
	return nil
}

func parseSocketEvent(packet string) (socketEvent, error) {
	payload := strings.TrimPrefix(packet, "42")
	for payload != "" && payload[0] >= '0' && payload[0] <= '9' {
		payload = payload[1:]
	}
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(payload), &arr); err != nil {
		return socketEvent{}, err
	}
	if len(arr) == 0 {
		return socketEvent{}, fmt.Errorf("empty socket event")
	}
	var name string
	if err := json.Unmarshal(arr[0], &name); err != nil {
		return socketEvent{}, err
	}
	var data json.RawMessage
	if len(arr) > 1 {
		data = arr[1]
	} else {
		data = json.RawMessage(`null`)
	}
	return socketEvent{Name: name, Data: data}, nil
}

func parseSocketAck(packet string) (int, json.RawMessage, error) {
	rest := strings.TrimPrefix(packet, "43")
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, nil, fmt.Errorf("socket ack missing id")
	}
	id, err := strconv.Atoi(rest[:i])
	if err != nil {
		return 0, nil, err
	}
	payload := strings.TrimSpace(rest[i:])
	var arr []json.RawMessage
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), &arr); err != nil {
			return 0, nil, err
		}
	}
	if len(arr) == 0 {
		return id, json.RawMessage(`null`), nil
	}
	return id, arr[0], nil
}

func (s *socketClient) emit(event string, payload any) error {
	data, err := json.Marshal([]any{event, payload})
	if err != nil {
		return err
	}
	return s.writePacket("42" + string(data))
}

func (s *socketClient) emitAck(ctx context.Context, event string, payload any) (json.RawMessage, error) {
	id, ch := s.registerAck()
	data, err := json.Marshal([]any{event, payload})
	if err != nil {
		s.unregisterAck(id)
		return nil, err
	}
	if err := s.writePacket("42" + strconv.Itoa(id) + string(data)); err != nil {
		s.unregisterAck(id)
		return nil, err
	}
	select {
	case <-ctx.Done():
		s.unregisterAck(id)
		return nil, ctx.Err()
	case payload, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("shadowob: websocket closed while waiting for ack")
		}
		return payload, nil
	}
}

func (s *socketClient) registerAck() (int, chan json.RawMessage) {
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	s.nextAck++
	id := s.nextAck
	ch := make(chan json.RawMessage, 1)
	s.ackChans[id] = ch
	return id, ch
}

func (s *socketClient) unregisterAck(id int) {
	s.ackMu.Lock()
	ch := s.ackChans[id]
	delete(s.ackChans, id)
	s.ackMu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (s *socketClient) deliverAck(id int, payload json.RawMessage) {
	s.ackMu.Lock()
	ch := s.ackChans[id]
	delete(s.ackChans, id)
	s.ackMu.Unlock()
	if ch != nil {
		ch <- payload
		close(ch)
	}
}

func (s *socketClient) writePacket(packet string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.conn == nil {
		return fmt.Errorf("shadowob: websocket is not connected")
	}
	if err := s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.TextMessage, []byte(packet))
}

func truncateLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
