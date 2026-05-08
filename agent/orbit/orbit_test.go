package orbit

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestParseSenderHeader(t *testing.T) {
	clean, user := parseSenderHeader("[cc-connect sender_id=U123 sender_name=\"Alice\" platform=slack chat_id=C1]\nhello")
	if clean != "hello" {
		t.Fatalf("clean = %q, want hello", clean)
	}
	if user == nil || user.ID != "U123" || user.Name != "Alice" || user.Platform != "slack" {
		t.Fatalf("unexpected user: %#v", user)
	}
}

func TestSessionSendsMessageSubmitAndMapsEvents(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "orbit-gateway-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socketPath := filepath.Join(dir, "gateway.sock")
	received := make(chan inboundRequest, 1)
	stop := startTestGateway(t, socketPath, func(req inboundRequest, w *bufio.Writer) {
		if req.Type == "message.submit" {
			received <- req
			writeTestEvent(t, w, outboundEvent{Type: "request.accepted", RequestID: req.RequestID, RoutedTo: "ask_anywhere"})
			writeTestEvent(t, w, outboundEvent{Type: "text.delta", RequestID: req.RequestID, Text: "hello"})
			writeTestEvent(t, w, outboundEvent{Type: "request.completed", RequestID: req.RequestID, Text: "done"})
		}
	})
	defer stop()

	agent, err := New(map[string]any{"socket_path": socketPath, "heartbeat_seconds": 0})
	if err != nil {
		t.Fatal(err)
	}
	inj := agent.(core.SessionEnvInjector)
	inj.SetSessionEnv([]string{"CC_SESSION_KEY=telegram:chat:user"})
	sess, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.Send("[cc-connect sender_id=user sender_name=\"Bob\" platform=telegram chat_id=chat]\n/ask hi", nil, nil); err != nil {
		t.Fatal(err)
	}

	select {
	case req := <-received:
		if req.SessionID != "telegram:chat:user" {
			t.Fatalf("sessionId = %q", req.SessionID)
		}
		if req.User == nil || req.User.ID != "user" || req.User.Name != "Bob" || req.User.Platform != "telegram" {
			t.Fatalf("unexpected user: %#v", req.User)
		}
		if req.Content == nil || req.Content.Kind != "text" || req.Content.Text != "/ask hi" {
			t.Fatalf("unexpected content: %#v", req.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request")
	}

	wantTypes := []core.EventType{core.EventThinking, core.EventText, core.EventResult}
	for _, want := range wantTypes {
		select {
		case evt := <-sess.Events():
			if evt.Type != want {
				t.Fatalf("event type = %s, want %s (event=%#v)", evt.Type, want, evt)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func TestSetWorkDirUpdatesDefaultSocketPathOnly(t *testing.T) {
	agent, err := New(map[string]any{"work_dir": "/tmp/orbit-a"})
	if err != nil {
		t.Fatal(err)
	}
	switcher := agent.(interface {
		SetWorkDir(string)
		WorkspaceAgentOptions() map[string]any
	})
	switcher.SetWorkDir("/tmp/orbit-b")
	opts := switcher.WorkspaceAgentOptions()
	if _, ok := opts["socket_path"]; ok {
		t.Fatalf("default socket_path should not be snapshotted: %#v", opts)
	}
	sess, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	got := sess.(*Session).cfg.socketPath
	want := filepath.Join("/tmp/orbit-b", ".orbit", "external-gateway.sock")
	if got != want {
		t.Fatalf("socketPath = %q, want %q", got, want)
	}

	explicit, err := New(map[string]any{
		"work_dir":    "/tmp/orbit-a",
		"socket_path": "/tmp/orbit-a/custom.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	explicitSwitcher := explicit.(interface {
		SetWorkDir(string)
		WorkspaceAgentOptions() map[string]any
	})
	explicitSwitcher.SetWorkDir("/tmp/orbit-b")
	explicitOpts := explicitSwitcher.WorkspaceAgentOptions()
	if explicitOpts["socket_path"] != "/tmp/orbit-a/custom.sock" {
		t.Fatalf("explicit socket_path changed: %#v", explicitOpts)
	}
}

func startTestGateway(t *testing.T, socketPath string, handler func(inboundRequest, *bufio.Writer)) func() {
	t.Helper()
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		writer := bufio.NewWriter(conn)
		for scanner.Scan() {
			var req inboundRequest
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			handler(req, writer)
		}
	}()
	return func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
}

func writeTestEvent(t *testing.T, w *bufio.Writer, evt outboundEvent) {
	t.Helper()
	b, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
}
