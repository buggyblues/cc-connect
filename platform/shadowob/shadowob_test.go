package shadowob

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestSocketURL(t *testing.T) {
	got, err := socketURL("http://localhost:3002/api")
	if err != nil {
		t.Fatalf("socketURL: %v", err)
	}
	want := "ws://localhost:3002/socket.io/?EIO=4&transport=websocket"
	if got != want {
		t.Fatalf("socketURL = %q, want %q", got, want)
	}
}

func TestParseSocketEvent(t *testing.T) {
	ev, err := parseSocketEvent(`42["message:new",{"id":"m1","content":"hello"}]`)
	if err != nil {
		t.Fatalf("parseSocketEvent: %v", err)
	}
	if ev.Name != "message:new" {
		t.Fatalf("event name = %q", ev.Name)
	}
	var payload shadowMessage
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if payload.ID != "m1" || payload.Content != "hello" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestParseSocketAck(t *testing.T) {
	id, payload, err := parseSocketAck(`431[{"ok":true}]`)
	if err != nil {
		t.Fatalf("parseSocketAck: %v", err)
	}
	if id != 1 {
		t.Fatalf("ack id = %d, want 1", id)
	}
	var res struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(payload, &res); err != nil {
		t.Fatalf("ack payload decode: %v", err)
	}
	if !res.OK {
		t.Fatalf("ack ok = false")
	}
}

func TestOptionStringList(t *testing.T) {
	opts := map[string]any{"channel_ids": []any{"c1", "c2", "c1", ""}}
	got := optionStringList(opts, "channel_ids")
	if len(got) != 2 || got[0] != "c1" || got[1] != "c2" {
		t.Fatalf("optionStringList = %#v", got)
	}

	opts = map[string]any{"channel_ids": "c1,c2 c3\nc4"}
	got = optionStringList(opts, "channel_ids")
	if len(got) != 4 {
		t.Fatalf("optionStringList string = %#v", got)
	}
}

func TestNewRequiresTokenAndDefaultsServerURL(t *testing.T) {
	if _, err := New(map[string]any{}); err == nil {
		t.Fatal("expected token requirement error")
	}
	platform, err := New(map[string]any{"token": "tok"})
	if err != nil {
		t.Fatalf("New with token: %v", err)
	}
	p := platform.(*Platform)
	if p.serverURL != "https://shadowob.com" {
		t.Fatalf("default serverURL = %q", p.serverURL)
	}
}

func TestMatchSlashCommandAndInteractivePrompt(t *testing.T) {
	commands := []shadowSlashCommand{{
		Name:        "deploy",
		Description: "Deploy the service",
		Body:        "Run deployment after validating the target.",
		Interaction: &shadowInteractiveBlock{
			ID:   "deploy_form",
			Kind: "form",
			Fields: []shadowInteractiveField{{
				ID:    "env",
				Kind:  "select",
				Label: "Environment",
			}},
		},
	}}
	match := matchSlashCommand("/deploy", commands)
	if match == nil {
		t.Fatal("expected slash command match")
	}
	prompt := formatSlashCommandPrompt("/deploy prod", &slashCommandMatch{
		Command:     commands[0],
		InvokedName: "deploy",
		Args:        "prod",
	})
	if prompt == "" || prompt[0] == '/' {
		t.Fatalf("prompt should be agent-facing text, got %q", prompt)
	}
}

func TestPublicSlashCommandsStripsBody(t *testing.T) {
	commands := publicSlashCommands([]shadowSlashCommand{{
		Name:        "deploy",
		Description: "Deploy",
		Body:        "secret local command body",
	}})
	if len(commands) != 1 {
		t.Fatalf("publicSlashCommands len = %d", len(commands))
	}
	if commands[0].Body != "" {
		t.Fatalf("public slash command body = %q, want empty", commands[0].Body)
	}
}

func TestHandlePolicyChangedIgnoresOtherAgents(t *testing.T) {
	p := &Platform{
		agentID: "agent-1",
		channels: map[string]channelRuntime{
			"ch1": {ID: "ch1", Policy: shadowChannelPolicy{Listen: true, Reply: true}},
		},
	}
	p.handlePolicyChanged(json.RawMessage(`{"agentId":"agent-2","channelId":"ch1","reply":false}`))
	if !p.channels["ch1"].Policy.Reply {
		t.Fatal("policy from another agent changed channel reply")
	}
	p.handlePolicyChanged(json.RawMessage(`{"agentId":"agent-1","channelId":"ch1","reply":false}`))
	if p.channels["ch1"].Policy.Reply {
		t.Fatal("policy from current agent did not update channel reply")
	}
}

func TestShadowClientSendMessageWithToken(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/channels/ch1/messages":
			authHeader = r.Header.Get("Authorization")
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if body["content"] != "hi" {
				t.Fatalf("content = %v", body["content"])
			}
			_ = json.NewEncoder(w).Encode(shadowMessage{ID: "m1", ChannelID: "ch1", Content: "hi"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newShadowClient(server.URL, "tok")
	msg, err := client.sendMessage(context.Background(), "ch1", "hi", sendMessageOptions{})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if msg.ID != "m1" {
		t.Fatalf("message id = %q", msg.ID)
	}
	if authHeader != "Bearer tok" {
		t.Fatalf("auth header = %q", authHeader)
	}
}

func TestSendFileUploadsForRemoteServer(t *testing.T) {
	var uploaded bool
	var msgAttachments []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/channels/ch1/messages":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if atts, ok := body["attachments"].([]any); ok {
				msgAttachments = atts
			}
			_ = json.NewEncoder(w).Encode(shadowMessage{ID: "m1", ChannelID: "ch1", Content: "\u200B"})
		case "/api/media/upload":
			uploaded = true
			if err := r.ParseMultipartForm(1024); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("form file: %v", err)
			}
			_ = file.Close()
			if header.Filename != "report.txt" {
				t.Fatalf("filename = %q", header.Filename)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(shadowUploadResponse{URL: "/shadow/uploads/report.txt", Size: 6})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := &Platform{
		serverURL:       "https://shadowob.com",
		client:          newShadowClient(server.URL, "tok"),
		sentDeliveryIDs: map[string]time.Time{},
	}
	err := p.SendFile(context.Background(), replyContext{channelID: "ch1"}, core.FileAttachment{
		Data:     []byte("report"),
		MimeType: "text/plain",
		FileName: "report.txt",
	})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if !uploaded {
		t.Fatal("remote server should upload media first")
	}
	if len(msgAttachments) != 1 {
		t.Fatalf("message should have 1 inline attachment, got %d", len(msgAttachments))
	}
}

func TestDownloadFileUsesTokenForRelativeContentRef(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shadow/uploads/report.txt" {
			http.NotFound(w, r)
			return
		}
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("report"))
	}))
	defer server.Close()

	client := newShadowClient(server.URL, "tok")
	data, ct, filename, err := client.downloadFile(context.Background(), "/shadow/uploads/report.txt", 1024)
	if err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	if authHeader != "Bearer tok" {
		t.Fatalf("auth header = %q", authHeader)
	}
	if string(data) != "report" {
		t.Fatalf("data = %q", data)
	}
	if ct != "text/plain" {
		t.Fatalf("content type = %q", ct)
	}
	if filename != "report.txt" {
		t.Fatalf("filename = %q", filename)
	}
}

func TestDownloadFileRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("too-large"))
	}))
	defer server.Close()

	client := newShadowClient(server.URL, "tok")
	if _, _, _, err := client.downloadFile(context.Background(), "/shadow/uploads/big.bin", 4); err == nil {
		t.Fatal("expected oversized download error")
	}
}

func TestResolveInboundMediaUsesSignedAttachmentURL(t *testing.T) {
	var resolved bool
	var downloaded bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/attachments/att1/media-url":
			resolved = true
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Fatalf("auth header = %q", r.Header.Get("Authorization"))
			}
			if r.URL.Query().Get("disposition") != "inline" {
				t.Fatalf("disposition = %q", r.URL.Query().Get("disposition"))
			}
			_ = json.NewEncoder(w).Encode(shadowSignedMediaURL{URL: "/api/media/signed/token"})
		case "/api/media/signed/token":
			downloaded = true
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := &Platform{
		client:        newShadowClient(server.URL, "tok"),
		mediaMaxBytes: defaultMediaMaxBytes,
	}
	images, files, audio, _ := p.resolveInboundMedia(context.Background(), shadowMessage{
		ChannelID: "ch1",
		Attachments: []shadowAttachment{{
			ID:          "att1",
			Filename:    "sample.png",
			URL:         "/shadow/uploads/sample.png",
			ContentType: "image/png",
			Size:        3,
		}},
	}, "look")
	if !resolved {
		t.Fatal("expected signed media URL resolution")
	}
	if !downloaded {
		t.Fatal("expected signed media download")
	}
	if len(images) != 1 || string(images[0].Data) != "png" || images[0].FileName != "sample.png" {
		t.Fatalf("images = %#v", images)
	}
	if len(files) != 0 || audio != nil {
		t.Fatalf("unexpected files/audio: %#v %#v", files, audio)
	}
}
