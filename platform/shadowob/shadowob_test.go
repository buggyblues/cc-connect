package shadowob

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestInteractiveResponseContentDoesNotDuplicateVisibleEcho(t *testing.T) {
	p := &Platform{}
	body := p.interactiveResponseContent(context.Background(), shadowMessage{
		ID:      "response-1",
		Content: "Use the submitted values as answers.\n- Q1: 1",
	}, map[string]any{
		"actionId": "submit",
		"values": map[string]any{
			"q1": "1",
		},
	})

	if !strings.Contains(body, "Submitted values") {
		t.Fatalf("interactive response body missing submitted values: %q", body)
	}
	if !strings.Contains(body, "Use the submitted values once.") {
		t.Fatalf("interactive response body missing de-dup instruction: %q", body)
	}
	if strings.Contains(body, "User message:") || strings.Contains(body, "- Q1: 1") {
		t.Fatalf("interactive response body duplicated visible echo: %q", body)
	}
}

func TestRegisterCommandsPublishesCoreCommands(t *testing.T) {
	var authHeader string
	var got struct {
		Commands []shadowSlashCommand `json:"commands"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agents/agent-1/slash-commands" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		authHeader = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	p := &Platform{
		agentID: "agent-1",
		client:  newShadowClient(server.URL, "tok"),
	}
	err := p.RegisterCommands([]core.BotCommandInfo{{
		Command:     "help",
		Description: "Show help",
	}})
	if err != nil {
		t.Fatalf("RegisterCommands: %v", err)
	}
	if authHeader != "Bearer tok" {
		t.Fatalf("auth header = %q", authHeader)
	}
	if len(got.Commands) != 1 {
		t.Fatalf("commands = %#v", got.Commands)
	}
	if got.Commands[0].Name != "help" || got.Commands[0].Description != "Show help" {
		t.Fatalf("registered command = %#v", got.Commands[0])
	}
	if got.Commands[0].PackID != "cc-connect" {
		t.Fatalf("pack id = %q, want cc-connect", got.Commands[0].PackID)
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

func TestMessageNewForKnownDMDispatchesAsDM(t *testing.T) {
	platform, err := New(map[string]any{"token": "tok", "allow_from": "*"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := platform.(*Platform)
	p.addDM("dm1")

	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}
	p.handleSocketEvent(context.Background(), socketEvent{
		Name: "message:new",
		Data: []byte(`{"id":"m1","channelId":"dm1","authorId":"u1","content":"hello"}`),
	})

	if got == nil {
		t.Fatal("expected message dispatch")
	}
	if got.ChannelKey != "shadowob:dm:dm1" || got.ChatName != "Shadow DM" {
		t.Fatalf("got channel key/chat = %q/%q", got.ChannelKey, got.ChatName)
	}
	rc, ok := got.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("reply context type = %T", got.ReplyCtx)
	}
	if rc.dmChannelID != "dm1" || rc.channelID != "dm1" {
		t.Fatalf("reply context = %#v", rc)
	}
}

func TestMessageNewSkipsOwnBotMessages(t *testing.T) {
	platform, err := New(map[string]any{"token": "tok", "allow_from": "*"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := platform.(*Platform)
	p.me = shadowUser{ID: "bot1", Username: "bot"}
	p.addDM("dm1")

	p.handler = func(_ core.Platform, msg *core.Message) {
		t.Fatalf("own message should not dispatch: %#v", msg)
	}
	p.handleSocketEvent(context.Background(), socketEvent{
		Name: "message:new",
		Data: []byte(`{"id":"m1","channelId":"dm1","authorId":"bot1","content":"self"}`),
	})
}

func TestMessageNewSkipsSentMessageEchoByID(t *testing.T) {
	platform, err := New(map[string]any{"token": "tok", "allow_from": "*"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := platform.(*Platform)
	p.addDM("dm1")
	p.recordSentMessageID(&shadowMessage{ID: "m1"})

	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}
	p.handleSocketEvent(context.Background(), socketEvent{
		Name: "message:new",
		Data: []byte(`{"id":"m1","channelId":"dm1","authorId":"u1","content":"self echo"}`),
	})
	if got != nil {
		t.Fatalf("sent message echo should not dispatch: %#v", got)
	}

	p.handleSocketEvent(context.Background(), socketEvent{
		Name: "message:new",
		Data: []byte(`{"id":"m2","channelId":"dm1","authorId":"u1","content":"hello"}`),
	})
	if got == nil {
		t.Fatal("expected a distinct message to dispatch")
	}
}

func TestChannelMessageClaimsHumanBuddyCollaborationAndRepliesWithMetadata(t *testing.T) {
	var claimReq claimBuddyReplyInput
	var sendBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/buddy-collaborations/claim":
			if r.Method != http.MethodPost {
				t.Fatalf("claim method = %s, want POST", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&claimReq); err != nil {
				t.Fatalf("decode claim: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":              true,
				"collaborationId": "collab-1",
				"turn":            1,
				"replyToId":       "root-1",
				"target":          "thread",
				"threadId":        "thread-collab",
				"metadata": map[string]any{
					"collaboration": map[string]any{
						"id":                 "collab-1",
						"rootMessageId":      "root-1",
						"buddyId":            "buddy-1",
						"turn":               1,
						"target":             "thread",
						"threadId":           "thread-collab",
						"suggestedTextLimit": 120,
						"replyDensity":       "brief",
					},
				},
			})
		case "/api/channels/ch1/messages":
			if r.Method != http.MethodPost {
				t.Fatalf("send method = %s, want POST", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&sendBody); err != nil {
				t.Fatalf("decode send: %v", err)
			}
			_ = json.NewEncoder(w).Encode(shadowMessage{ID: "bot-msg-1", ChannelID: "ch1", Content: "done"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := newShadowOBTestPlatform(t, server.URL)
	p.channels["ch1"] = channelRuntime{
		ID:   "ch1",
		Name: "general",
		Policy: shadowChannelPolicy{
			Listen: true,
			Reply:  true,
			Config: map[string]any{"maxBuddyTurns": 3},
		},
	}

	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}
	p.handleChannelMessage(context.Background(), shadowMessage{
		ID:        "root-1",
		ChannelID: "ch1",
		AuthorID:  "human-1",
		Content:   "hello",
		Author:    &shadowAuthor{ID: "human-1", Username: "alice"},
	})

	if claimReq.Mode != "initial" || claimReq.RootMessageID != "root-1" || claimReq.ReplyToMessageID != "root-1" {
		t.Fatalf("claim request = %#v", claimReq)
	}
	if claimReq.BuddyID != "buddy-1" || claimReq.MaxTurns != 3 {
		t.Fatalf("claim buddy/max turns = %#v", claimReq)
	}
	if got == nil {
		t.Fatal("expected dispatch after successful claim")
	}
	if got.ChannelKey != "shadowob:channel:ch1:thread:thread-collab" {
		t.Fatalf("channel key = %q", got.ChannelKey)
	}
	if !strings.Contains(got.ExtraContent, "Shadow Buddy collaboration context") {
		t.Fatalf("missing collaboration prompt: %q", got.ExtraContent)
	}
	rc, ok := got.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("reply context type = %T", got.ReplyCtx)
	}
	if rc.threadID != "thread-collab" || rc.replyToID != "root-1" {
		t.Fatalf("reply context = %#v", rc)
	}
	if rc.collaboration == nil || rc.collaboration.ID != "collab-1" {
		t.Fatalf("collaboration = %#v", rc.collaboration)
	}

	if err := p.Reply(context.Background(), got.ReplyCtx, "done"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if sendBody["threadId"] != "thread-collab" || sendBody["replyToId"] != "root-1" {
		t.Fatalf("send target = %#v", sendBody)
	}
	metadata, ok := sendBody["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v", sendBody["metadata"])
	}
	collaboration, ok := metadata["collaboration"].(map[string]any)
	if !ok {
		t.Fatalf("collaboration metadata = %#v", metadata["collaboration"])
	}
	if collaboration["id"] != "collab-1" || collaboration["rootMessageId"] != "root-1" {
		t.Fatalf("collaboration metadata = %#v", collaboration)
	}
}

func TestBuddyMessageWithoutCollaborationIsSkipped(t *testing.T) {
	claimCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/buddy-collaborations/claim" {
			claimCount++
			t.Fatalf("claim should not be called without collaboration metadata")
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	p := newShadowOBTestPlatform(t, server.URL)
	p.channels["ch1"] = channelRuntime{
		ID: "ch1",
		Policy: shadowChannelPolicy{
			Listen: true,
			Reply:  true,
		},
	}
	p.handler = func(_ core.Platform, msg *core.Message) {
		t.Fatalf("Buddy message without collaboration should not dispatch: %#v", msg)
	}
	p.handleChannelMessage(context.Background(), shadowMessage{
		ID:        "buddy-msg-1",
		ChannelID: "ch1",
		AuthorID:  "buddy-2",
		Content:   "I can help",
		Author:    &shadowAuthor{ID: "buddy-2", Username: "other-buddy", IsBot: true},
	})
	if claimCount != 0 {
		t.Fatalf("claim count = %d, want 0", claimCount)
	}
}

func TestBuddyMessageWithCollaborationHonorsExplicitReplyToBuddyFalse(t *testing.T) {
	claimCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/buddy-collaborations/claim" {
			claimCount++
			t.Fatalf("claim should not be called when replyToBuddy=false")
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	p := newShadowOBTestPlatform(t, server.URL)
	p.channels["ch1"] = channelRuntime{
		ID: "ch1",
		Policy: shadowChannelPolicy{
			Listen: true,
			Reply:  true,
			Config: map[string]any{"replyToBuddy": false},
		},
	}
	p.handler = func(_ core.Platform, msg *core.Message) {
		t.Fatalf("Buddy message should not dispatch when replyToBuddy=false: %#v", msg)
	}
	p.handleChannelMessage(context.Background(), shadowMessage{
		ID:        "buddy-msg-1",
		ChannelID: "ch1",
		AuthorID:  "buddy-2",
		Content:   "One more point.",
		Author:    &shadowAuthor{ID: "buddy-2", Username: "other-buddy", IsBot: true},
		Metadata: map[string]any{
			"collaboration": map[string]any{
				"id":            "collab-1",
				"rootMessageId": "root-1",
				"buddyId":       "buddy-2",
				"turn":          1,
			},
		},
	})
	if claimCount != 0 {
		t.Fatalf("claim count = %d, want 0", claimCount)
	}
}

func TestExplicitMentionOverridesDisabledReplyPolicy(t *testing.T) {
	var claimReq claimBuddyReplyInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/buddy-collaborations/claim" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&claimReq); err != nil {
			t.Fatalf("decode claim: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":              true,
			"collaborationId": "collab-mention",
			"turn":            1,
			"replyToId":       "root-mention",
			"target":          "main",
		})
	}))
	defer server.Close()

	p := newShadowOBTestPlatform(t, server.URL)
	p.me = shadowUser{ID: "buddy-user", Username: "buddy"}
	p.channels["ch1"] = channelRuntime{
		ID: "ch1",
		Policy: shadowChannelPolicy{
			Listen:      true,
			Reply:       false,
			MentionOnly: true,
		},
	}
	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}
	p.handleChannelMessage(context.Background(), shadowMessage{
		ID:        "root-mention",
		ChannelID: "ch1",
		AuthorID:  "human-1",
		Content:   "@buddy please check",
		Author:    &shadowAuthor{ID: "human-1", Username: "alice"},
	})
	if claimReq.Mode != "initial" || claimReq.RootMessageID != "root-mention" {
		t.Fatalf("claim request = %#v", claimReq)
	}
	if got == nil {
		t.Fatal("expected explicit mention to dispatch despite disabled reply policy")
	}
}

func TestTaskCardDispatchesAndThreadCommentReusesTaskSession(t *testing.T) {
	var claimCalled, updateCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/messages/root-1/cards/card-1/claim":
			claimCalled = true
			_ = json.NewEncoder(w).Encode(shadowMessage{
				ID:        "root-1",
				ChannelID: "ch1",
				Content:   "Task title\n\nTask body",
				Metadata: map[string]any{
					"cards": []any{map[string]any{
						"kind":     "task",
						"id":       "card-1",
						"title":    "Task title",
						"body":     "Task body",
						"status":   "claimed",
						"assignee": map[string]any{"agentId": "buddy-1"},
						"data":     map[string]any{"task": map[string]any{"threadId": "thread-1"}},
					}},
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/messages/root-1/cards/card-1":
			updateCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode update: %v", err)
			}
			if body["status"] != "running" {
				t.Fatalf("task status update = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(shadowMessage{
				ID:        "root-1",
				ChannelID: "ch1",
				Content:   "Task title\n\nTask body",
				Metadata: map[string]any{
					"cards": []any{map[string]any{
						"kind":     "task",
						"id":       "card-1",
						"title":    "Task title",
						"body":     "Task body",
						"status":   "running",
						"assignee": map[string]any{"agentId": "buddy-1"},
						"data":     map[string]any{"task": map[string]any{"threadId": "thread-1"}},
					}},
				},
			})
		case r.URL.Path == "/api/buddy-collaborations/claim":
			t.Fatalf("task context should not call collaboration claim")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := newShadowOBTestPlatform(t, server.URL)
	p.me = shadowUser{ID: "buddy-user", Username: "buddy"}
	p.channels["ch1"] = channelRuntime{
		ID: "ch1",
		Policy: shadowChannelPolicy{
			Listen:      true,
			Reply:       false,
			MentionOnly: true,
		},
	}
	var got []*core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = append(got, msg)
	}

	p.handleChannelMessage(context.Background(), shadowMessage{
		ID:        "root-1",
		ChannelID: "ch1",
		AuthorID:  "human-1",
		Content:   "Task title\n\nTask body",
		Author:    &shadowAuthor{ID: "human-1", Username: "alice"},
		Metadata: map[string]any{
			"cards": []any{map[string]any{
				"kind":     "task",
				"id":       "card-1",
				"title":    "Task title",
				"body":     "Task body",
				"status":   "queued",
				"assignee": map[string]any{"agentId": "buddy-1"},
				"data":     map[string]any{"task": map[string]any{"threadId": "thread-1"}},
			}},
		},
	})

	if !claimCalled || !updateCalled {
		t.Fatalf("claim/update called = %v/%v", claimCalled, updateCalled)
	}
	if len(got) != 1 {
		t.Fatalf("messages dispatched after task = %d", len(got))
	}
	wantSession := "shadowob:task:channel:ch1:thread:thread-1:message:root-1:card:card-1"
	if got[0].SessionKey != wantSession {
		t.Fatalf("task session key = %q", got[0].SessionKey)
	}
	if !strings.Contains(got[0].Content, "[Shadow Inbox task]") {
		t.Fatalf("missing task prompt: %q", got[0].Content)
	}
	rc, ok := got[0].ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("reply context type = %T", got[0].ReplyCtx)
	}
	if rc.threadID != "thread-1" || rc.replyToID != "root-1" {
		t.Fatalf("task reply context = %#v", rc)
	}

	p.handleChannelMessage(context.Background(), shadowMessage{
		ID:        "comment-1",
		ChannelID: "ch1",
		ThreadID:  "thread-1",
		AuthorID:  "human-1",
		Content:   "Please answer FOLLOWUP.",
		Author:    &shadowAuthor{ID: "human-1", Username: "alice"},
	})

	if len(got) != 2 {
		t.Fatalf("messages dispatched after thread comment = %d", len(got))
	}
	if got[1].SessionKey != wantSession {
		t.Fatalf("thread comment session key = %q", got[1].SessionKey)
	}
	if !strings.Contains(got[1].Content, "[Shadow Inbox task thread comment]") ||
		!strings.Contains(got[1].Content, "Please answer FOLLOWUP.") {
		t.Fatalf("missing task thread prompt: %q", got[1].Content)
	}
}

func TestBuddyMessageWithCollaborationClaimsConversationTurn(t *testing.T) {
	var claimReq claimBuddyReplyInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/buddy-collaborations/claim" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&claimReq); err != nil {
			t.Fatalf("decode claim: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":              true,
			"collaborationId": "collab-1",
			"turn":            2,
			"replyToId":       "buddy-msg-1",
			"target":          "thread",
			"threadId":        "thread-collab",
		})
	}))
	defer server.Close()

	p := newShadowOBTestPlatform(t, server.URL)
	p.channels["ch1"] = channelRuntime{
		ID: "ch1",
		Policy: shadowChannelPolicy{
			Listen: true,
			Reply:  true,
			Config: map[string]any{"maxBuddyTurns": 2},
		},
	}
	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}
	p.handleChannelMessage(context.Background(), shadowMessage{
		ID:        "buddy-msg-1",
		ChannelID: "ch1",
		AuthorID:  "buddy-2",
		Content:   "One more point.",
		Author:    &shadowAuthor{ID: "buddy-2", Username: "other-buddy", IsBot: true},
		Metadata: map[string]any{
			"collaboration": map[string]any{
				"id":            "collab-1",
				"rootMessageId": "root-1",
				"buddyId":       "buddy-2",
				"turn":          1,
				"target":        "thread",
				"threadId":      "thread-collab",
			},
		},
	})

	if claimReq.Mode != "conversation" || claimReq.RootMessageID != "root-1" || claimReq.ReplyToMessageID != "buddy-msg-1" {
		t.Fatalf("claim request = %#v", claimReq)
	}
	if claimReq.MaxTurns != 2 {
		t.Fatalf("max turns = %d, want 2", claimReq.MaxTurns)
	}
	if got == nil {
		t.Fatal("expected conversation dispatch")
	}
	if !strings.Contains(got.ExtraContent, "This Buddy turn: 2") {
		t.Fatalf("missing turn context: %q", got.ExtraContent)
	}
	rc, ok := got.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("reply context type = %T", got.ReplyCtx)
	}
	if rc.replyToID != "buddy-msg-1" || rc.threadID != "thread-collab" {
		t.Fatalf("reply context = %#v", rc)
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

func TestReconstructReplyCtxTaskExplicitThreadKey(t *testing.T) {
	var sendBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/channels/ch1/messages":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&sendBody); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(shadowMessage{ID: "reply-1", ChannelID: "ch1", ThreadID: "thread-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := newShadowOBTestPlatform(t, server.URL)
	replyCtx, err := p.ReconstructReplyCtx("shadowob:task:channel:ch1:thread:thread-1:message:root-1:card:card-1")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx: %v", err)
	}
	rc, ok := replyCtx.(replyContext)
	if !ok {
		t.Fatalf("reply context type = %T", replyCtx)
	}
	if rc.channelID != "ch1" || rc.threadID != "thread-1" || rc.replyToID != "root-1" {
		t.Fatalf("reply context = %#v", rc)
	}
	if err := p.Reply(context.Background(), replyCtx, "done"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if sendBody["threadId"] != "thread-1" || sendBody["replyToId"] != "root-1" {
		t.Fatalf("send body = %#v", sendBody)
	}
}

func TestReconstructReplyCtxTaskLegacyWorkspaceKeyResolvesMessage(t *testing.T) {
	var requestedMessage bool
	var sendBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/messages/root-1":
			requestedMessage = true
			_ = json.NewEncoder(w).Encode(shadowMessage{
				ID:        "root-1",
				ChannelID: "ch1",
				Metadata: map[string]any{
					"cards": []any{
						map[string]any{
							"kind":     "task",
							"id":       "card-1",
							"threadId": "thread-1",
						},
					},
				},
			})
		case "/api/channels/ch1/messages":
			if err := json.NewDecoder(r.Body).Decode(&sendBody); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(shadowMessage{ID: "reply-1", ChannelID: "ch1", ThreadID: "thread-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := newShadowOBTestPlatform(t, server.URL)
	replyCtx, err := p.ReconstructReplyCtx("shadowob:task:workspace-1:root-1:card-1")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx: %v", err)
	}
	if !requestedMessage {
		t.Fatal("expected message lookup")
	}
	rc, ok := replyCtx.(replyContext)
	if !ok {
		t.Fatalf("reply context type = %T", replyCtx)
	}
	if rc.channelID != "ch1" || rc.threadID != "thread-1" || rc.replyToID != "root-1" {
		t.Fatalf("reply context = %#v", rc)
	}
	if err := p.Reply(context.Background(), replyCtx, "done"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if sendBody["threadId"] != "thread-1" || sendBody["replyToId"] != "root-1" {
		t.Fatalf("send body = %#v", sendBody)
	}
}

func TestShadowClientUsesCurrentDMRoutes(t *testing.T) {
	var sawList bool
	var sawSend bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/channels/dm":
			if r.Method != http.MethodGet {
				t.Fatalf("list DM method = %s", r.Method)
			}
			sawList = true
			_ = json.NewEncoder(w).Encode([]shadowDMChannel{{ID: "dm1", UserAID: "u1", UserBID: "u2"}})
		case "/api/channels/dm1/messages":
			if r.Method != http.MethodPost {
				t.Fatalf("send DM method = %s", r.Method)
			}
			sawSend = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if body["content"] != "hi" {
				t.Fatalf("content = %v", body["content"])
			}
			if body["threadId"] != "thread1" {
				t.Fatalf("threadId = %v", body["threadId"])
			}
			_ = json.NewEncoder(w).Encode(shadowMessage{ID: "m1", ChannelID: "dm1", Content: "hi"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newShadowClient(server.URL, "tok")
	dms, err := client.listDMChannels(context.Background())
	if err != nil {
		t.Fatalf("listDMChannels: %v", err)
	}
	if len(dms) != 1 || dms[0].ID != "dm1" {
		t.Fatalf("dms = %#v", dms)
	}
	msg, err := client.sendDMMessage(context.Background(), "dm1", "hi", sendMessageOptions{ThreadID: "thread1"})
	if err != nil {
		t.Fatalf("sendDMMessage: %v", err)
	}
	if msg.ID != "m1" || msg.DMChannelID != "dm1" {
		t.Fatalf("message = %#v", msg)
	}
	if !sawList || !sawSend {
		t.Fatalf("expected both DM routes to be hit, list=%v send=%v", sawList, sawSend)
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
		DMChannelID: "dm1",
		Attachments: []shadowAttachment{
			{
				ID:          "att1",
				Filename:    "sample.png",
				URL:         "/shadow/uploads/sample.png",
				ContentType: "image/png",
				Size:        3,
			},
		},
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

func newShadowOBTestPlatform(t *testing.T, serverURL string) *Platform {
	t.Helper()
	platform, err := New(map[string]any{
		"token":      "tok",
		"allow_from": "*",
		"agent_id":   "buddy-1",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := platform.(*Platform)
	p.client = newShadowClient(serverURL, "tok")
	return p
}
