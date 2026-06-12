package shadowob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("shadowob", New)
}

const (
	defaultReconnectDelay = time.Second
	maxReconnectDelay     = 30 * time.Second
	defaultMediaMaxBytes  = 20 << 20
)

var (
	htmlTagRE       = regexp.MustCompile(`<[^>]+>`)
	markdownMediaRE = regexp.MustCompile(`!?\[[^\]]*\]\(([^)]+)\)`)
)

type replyContext struct {
	channelID     string
	dmChannelID   string
	threadID      string
	messageID     string
	replyToID     string
	sessionKey    string
	taskMessageID string
	taskCardID    string
	taskComplete  bool
	discussion    *buddyThreadDiscussionState
}

type taskThreadBinding struct {
	channelID string
	threadID  string
	messageID string
	cardID    string
	title     string
}

type previewHandle struct {
	messageID string
}

type channelRuntime struct {
	ID       string
	Name     string
	ServerID string
	Policy   shadowChannelPolicy
}

type Platform struct {
	serverURL             string
	token                 string
	allowFrom             string
	agentID               string
	shareSessionInChannel bool
	listenDMs             bool
	autoJoinAllChannels   bool
	useAgentConfig        bool
	progressStyle         string
	mediaMaxBytes         int64
	slashCommandsPath     string

	configChannelIDs []string
	configServerIDs  []string
	configDMIDs      []string

	client *shadowClient

	mu                  sync.RWMutex
	handler             core.MessageHandler
	lifecycleHandler    core.PlatformLifecycleHandler
	ctx                 context.Context
	cancel              context.CancelFunc
	socket              *socketClient
	generation          uint64
	everConnected       bool
	unavailableNotified bool
	me                  shadowUser
	channels            map[string]channelRuntime
	dmChannels          map[string]bool
	coreCommands        []shadowSlashCommand
	localCommands       []shadowSlashCommand
	sentDeliveryIDs     map[string]time.Time
	lastDeliverySweep   time.Time
	sentMessageIDs      map[string]time.Time
	lastSentMsgSweep    time.Time
	receivedMsgIDs      map[string]time.Time
	lastReceivedSweep   time.Time
	previewMsgs         map[string]string
	taskThreadBindings  map[string]taskThreadBinding
}

func New(opts map[string]any) (core.Platform, error) {
	serverURL := optionString(opts, "server_url", "https://shadowob.com")
	token := optionString(opts, "token", "")
	if token == "" {
		return nil, fmt.Errorf("shadowob: token is required")
	}

	allowFrom := optionString(opts, "allow_from", "")
	core.CheckAllowFrom("shadowob", allowFrom)

	agentID := optionString(opts, "agent_id", "")
	useAgentConfig := optionBool(opts, "use_agent_config", agentID != "")
	listenDMs := optionBool(opts, "listen_dms", true)
	progressStyle := optionString(opts, "progress_style", "compact")
	if progressStyle == "" {
		progressStyle = "compact"
	}
	mediaMaxBytes := int64(optionInt(opts, "media_max_bytes", defaultMediaMaxBytes))
	if mediaMaxBytes <= 0 {
		mediaMaxBytes = defaultMediaMaxBytes
	}
	slashPath := optionString(opts, "slash_commands_path", "")
	if slashPath == "" {
		slashPath = strings.TrimSpace(getenv("SHADOW_SLASH_COMMANDS_PATH"))
	}

	localCommands, err := loadSlashCommandsFile(slashPath)
	if err != nil {
		return nil, fmt.Errorf("shadowob: load slash_commands_path: %w", err)
	}

	return &Platform{
		serverURL:             normalizeServerURL(serverURL),
		token:                 token,
		allowFrom:             allowFrom,
		agentID:               agentID,
		shareSessionInChannel: optionBool(opts, "share_session_in_channel", false),
		listenDMs:             listenDMs,
		autoJoinAllChannels:   optionBool(opts, "auto_join_all_channels", false),
		useAgentConfig:        useAgentConfig,
		progressStyle:         progressStyle,
		mediaMaxBytes:         mediaMaxBytes,
		slashCommandsPath:     slashPath,
		configChannelIDs:      optionStringList(opts, "channel_ids"),
		configServerIDs:       optionStringList(opts, "server_ids"),
		configDMIDs:           optionStringList(opts, "dm_channel_ids"),
		channels:              make(map[string]channelRuntime),
		dmChannels:            make(map[string]bool),
		localCommands:         localCommands,
		sentDeliveryIDs:       make(map[string]time.Time),
		sentMessageIDs:        make(map[string]time.Time),
		receivedMsgIDs:        make(map[string]time.Time),
		previewMsgs:           make(map[string]string),
		taskThreadBindings:    make(map[string]taskThreadBinding),
	}, nil
}

func (p *Platform) Name() string { return "shadowob" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	if p.cancel != nil {
		p.mu.Unlock()
		return fmt.Errorf("shadowob: already started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	p.cancel = cancel
	p.handler = handler
	p.client = newShadowClient(p.serverURL, p.token)
	p.mu.Unlock()

	if err := p.authenticate(ctx); err != nil {
		cancel()
		return err
	}
	if err := p.loadInitialRoutes(ctx); err != nil {
		cancel()
		return err
	}
	if err := p.registerSlashCommands(ctx); err != nil {
		slog.Warn("shadowob: slash command registration failed", "error", err)
	}

	go p.connectLoop(ctx)
	if p.agentID != "" {
		go p.heartbeatLoop(ctx)
	}
	slog.Info("shadowob: platform started", "server_url", p.serverURL, "channels", len(p.channels), "dms", len(p.dmChannels), "agent_id", p.agentID)
	return nil
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	cancel := p.cancel
	socket := p.socket
	p.cancel = nil
	p.socket = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if socket != nil {
		socket.close()
	}
	return nil
}

func (p *Platform) authenticate(ctx context.Context) error {
	client := p.client
	reqCtx, cancel := requestContext(ctx)
	me, err := client.getMe(reqCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("shadowob: get me: %w", err)
	}
	p.mu.Lock()
	p.me = *me
	if p.agentID == "" {
		p.agentID = me.AgentID
	}
	if !p.useAgentConfig && p.agentID != "" && len(p.configChannelIDs) == 0 {
		p.useAgentConfig = true
	}
	p.mu.Unlock()
	return nil
}

func (p *Platform) loadInitialRoutes(ctx context.Context) error {
	p.mu.Lock()
	p.channels = make(map[string]channelRuntime)
	p.dmChannels = make(map[string]bool)
	p.mu.Unlock()

	for _, channelID := range p.configChannelIDs {
		p.addChannel(channelID, "", "", shadowChannelPolicy{Listen: true, Reply: true})
		p.resolveChannelName(ctx, channelID)
	}
	for _, dmID := range p.configDMIDs {
		p.addDM(dmID)
	}
	if len(p.configServerIDs) > 0 {
		for _, serverID := range p.configServerIDs {
			if err := p.addServerChannels(ctx, serverID); err != nil {
				return err
			}
		}
	}
	if p.useAgentConfig && p.agentID != "" {
		if err := p.addAgentConfigChannels(ctx); err != nil {
			slog.Warn("shadowob: agent config unavailable", "agent_id", p.agentID, "error", err)
		}
	}
	if p.autoJoinAllChannels {
		if err := p.addAllUserChannels(ctx); err != nil {
			return err
		}
	}
	if p.listenDMs {
		p.refreshDMChannels(ctx)
	}
	return nil
}

func (p *Platform) addAgentConfigChannels(ctx context.Context) error {
	reqCtx, cancel := requestContext(ctx)
	remote, err := p.client.getAgentConfig(reqCtx, p.agentID)
	cancel()
	if err != nil {
		return err
	}
	for _, server := range remote.Servers {
		for _, ch := range server.Channels {
			if !ch.Policy.Listen {
				continue
			}
			p.addChannel(ch.ID, ch.Name, server.ID, ch.Policy)
		}
	}
	return nil
}

func (p *Platform) addAllUserChannels(ctx context.Context) error {
	reqCtx, cancel := requestContext(ctx)
	servers, err := p.client.listServers(reqCtx)
	cancel()
	if err != nil {
		return err
	}
	for _, server := range servers {
		if err := p.addServerChannels(ctx, server.ID); err != nil {
			return err
		}
	}
	return nil
}

func (p *Platform) addServerChannels(ctx context.Context, serverID string) error {
	reqCtx, cancel := requestContext(ctx)
	channels, err := p.client.getServerChannels(reqCtx, serverID)
	cancel()
	if err != nil {
		return err
	}
	for _, ch := range channels {
		p.addChannel(ch.ID, ch.Name, ch.ServerID, shadowChannelPolicy{Listen: true, Reply: true})
	}
	return nil
}

func (p *Platform) addChannel(id, name, serverID string, policy shadowChannelPolicy) {
	if id == "" {
		return
	}
	p.mu.Lock()
	p.channels[id] = channelRuntime{ID: id, Name: name, ServerID: serverID, Policy: policy}
	p.mu.Unlock()
}

func (p *Platform) resolveChannelName(ctx context.Context, channelID string) {
	reqCtx, cancel := requestContext(ctx)
	ch, err := p.client.getChannel(reqCtx, channelID)
	cancel()
	if err != nil {
		slog.Debug("shadowob: resolve channel failed", "channel_id", channelID, "error", err)
		return
	}
	p.mu.Lock()
	rt := p.channels[channelID]
	rt.Name = ch.Name
	rt.ServerID = ch.ServerID
	p.channels[channelID] = rt
	p.mu.Unlock()
}

func (p *Platform) addDM(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	p.dmChannels[id] = true
	p.mu.Unlock()
}

func (p *Platform) refreshDMChannels(ctx context.Context) {
	reqCtx, cancel := requestContext(ctx)
	channels, err := p.client.listDMChannels(reqCtx)
	cancel()
	if err != nil {
		slog.Warn("shadowob: list DM channels failed", "error", err)
		return
	}
	for _, ch := range channels {
		p.addDM(ch.ID)
	}
}

func (p *Platform) connectLoop(ctx context.Context) {
	delay := defaultReconnectDelay
	for ctx.Err() == nil {
		err := p.runSocket(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("shadowob: websocket disconnected", "error", err, "reconnect_in", delay)
		p.notifyUnavailable(err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay *= 2
		if delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}
	}
}

func (p *Platform) runSocket(ctx context.Context) error {
	socket := newSocketClient(p.serverURL, p.token)
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err := socket.connect(dialCtx)
	cancel()
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.socket = socket
	p.generation++
	gen := p.generation
	p.everConnected = true
	p.unavailableNotified = false
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.socket == socket {
			p.socket = nil
		}
		p.mu.Unlock()
	}()

	onConnect := func() {
		slog.Info("shadowob: websocket connected")
		p.joinRooms(ctx, socket)
		p.notifyReady(gen, socket)
	}
	onEvent := func(ev socketEvent) {
		p.handleSocketEvent(ctx, ev)
	}
	return socket.readLoop(ctx, onConnect, onEvent)
}

func (p *Platform) joinRooms(ctx context.Context, socket *socketClient) {
	p.mu.RLock()
	channelIDs := make([]string, 0, len(p.channels))
	for id := range p.channels {
		channelIDs = append(channelIDs, id)
	}
	dmIDs := make([]string, 0, len(p.dmChannels))
	for id := range p.dmChannels {
		dmIDs = append(dmIDs, id)
	}
	p.mu.RUnlock()

	for _, channelID := range channelIDs {
		joinCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ack, err := socket.emitAck(joinCtx, "channel:join", map[string]string{"channelId": channelID})
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				slog.Debug("shadowob: channel join ack not received", "channel_id", channelID)
				continue
			}
			slog.Warn("shadowob: channel join failed", "channel_id", channelID, "error", err)
			continue
		}
		var res struct {
			OK bool `json:"ok"`
		}
		if len(ack) > 0 && string(ack) != "null" {
			_ = json.Unmarshal(ack, &res)
		} else {
			res.OK = true
		}
		if !res.OK {
			slog.Warn("shadowob: channel join denied", "channel_id", channelID)
		}
	}
	for _, dmID := range dmIDs {
		joinCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ack, err := socket.emitAck(joinCtx, "channel:join", map[string]string{"channelId": dmID})
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				slog.Debug("shadowob: DM channel join ack not received", "dm_channel_id", dmID)
				continue
			}
			slog.Warn("shadowob: DM channel join failed", "dm_channel_id", dmID, "error", err)
			continue
		}
		var res struct {
			OK bool `json:"ok"`
		}
		if len(ack) > 0 && string(ack) != "null" {
			_ = json.Unmarshal(ack, &res)
		} else {
			res.OK = true
		}
		if !res.OK {
			slog.Warn("shadowob: DM channel join denied", "dm_channel_id", dmID)
		}
	}
}

func (p *Platform) handleSocketEvent(ctx context.Context, ev socketEvent) {
	switch ev.Name {
	case "message:new":
		var msg shadowMessage
		if err := json.Unmarshal(ev.Data, &msg); err != nil {
			slog.Warn("shadowob: decode message:new failed", "error", err)
			return
		}
		if p.isDMMessage(msg) {
			p.handleDMMessage(ctx, msg)
			return
		}
		p.handleChannelMessage(ctx, msg)
	case "dm:message:new", "dm:message":
		var msg shadowMessage
		if err := json.Unmarshal(ev.Data, &msg); err != nil {
			slog.Warn("shadowob: decode dm message failed", "error", err)
			return
		}
		p.handleDMMessage(ctx, msg)
	case "channel:member-added":
		var data struct {
			ChannelID string `json:"channelId"`
			ServerID  string `json:"serverId"`
		}
		if json.Unmarshal(ev.Data, &data) == nil && data.ChannelID != "" {
			p.addChannel(data.ChannelID, "", data.ServerID, shadowChannelPolicy{Listen: true, Reply: true})
			p.resolveChannelName(ctx, data.ChannelID)
			p.mu.RLock()
			socket := p.socket
			p.mu.RUnlock()
			if socket != nil {
				_ = socket.emit("channel:join", map[string]string{"channelId": data.ChannelID})
			}
		}
	case "channel:member-removed":
		var data struct {
			ChannelID string `json:"channelId"`
		}
		if json.Unmarshal(ev.Data, &data) == nil && data.ChannelID != "" {
			p.mu.Lock()
			delete(p.channels, data.ChannelID)
			p.mu.Unlock()
		}
	case "agent:policy-changed":
		p.handlePolicyChanged(ev.Data)
	case "error":
		slog.Warn("shadowob: socket error event", "payload", string(ev.Data))
	}
}

func (p *Platform) isDMMessage(sm shadowMessage) bool {
	if sm.DMChannelID != "" {
		return true
	}
	if sm.ChannelID == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dmChannels[sm.ChannelID]
}

func (p *Platform) handlePolicyChanged(data json.RawMessage) {
	var payload struct {
		AgentID     string         `json:"agentId"`
		ChannelID   string         `json:"channelId"`
		MentionOnly *bool          `json:"mentionOnly"`
		Reply       *bool          `json:"reply"`
		Config      map[string]any `json:"config"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.ChannelID == "" {
		return
	}
	p.mu.RLock()
	agentID := p.agentID
	p.mu.RUnlock()
	if agentID != "" && payload.AgentID != "" && payload.AgentID != agentID {
		return
	}
	p.mu.Lock()
	rt := p.channels[payload.ChannelID]
	rt.ID = payload.ChannelID
	rt.Policy.Listen = true
	if payload.MentionOnly != nil {
		rt.Policy.MentionOnly = *payload.MentionOnly
	}
	if payload.Reply != nil {
		rt.Policy.Reply = *payload.Reply
	}
	if payload.Config != nil {
		rt.Policy.Config = payload.Config
	}
	p.channels[payload.ChannelID] = rt
	p.mu.Unlock()
}

func isTerminalTaskStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "canceled", "transferred":
		return true
	default:
		return false
	}
}

func taskCardID(card map[string]any) string {
	return firstNonEmpty(stringValue(card["id"]), stringValue(card["cardId"]), stringValue(card["card_id"]))
}

func taskThreadIDFromCard(card map[string]any) string {
	if threadID := stringMapValue(card, "threadId", "thread_id", "taskThreadId"); threadID != "" {
		return threadID
	}
	for _, key := range []string{"data", "target"} {
		nested, _ := card[key].(map[string]any)
		if nested == nil {
			continue
		}
		if threadID := stringMapValue(nested, "threadId", "thread_id", "taskThreadId"); threadID != "" {
			return threadID
		}
		if task, _ := nested["task"].(map[string]any); task != nil {
			if threadID := stringMapValue(task, "threadId", "thread_id", "taskThreadId"); threadID != "" {
				return threadID
			}
		}
	}
	return ""
}

func runtimeTaskCardForSelf(sm shadowMessage, buddyUserID, agentID string) map[string]any {
	if len(sm.Metadata) == 0 {
		return nil
	}
	cards, _ := sm.Metadata["cards"].([]any)
	for _, raw := range cards {
		card, ok := raw.(map[string]any)
		if !ok || card["kind"] != "task" {
			continue
		}
		if isTerminalTaskStatus(stringValue(card["status"])) {
			continue
		}
		assignee, _ := card["assignee"].(map[string]any)
		if assignee == nil {
			return card
		}
		assignedUser := stringMapValue(assignee, "userId", "user_id")
		assignedAgent := stringMapValue(assignee, "agentId", "agent_id")
		if buddyUserID != "" && assignedUser == buddyUserID {
			return card
		}
		if agentID != "" && assignedAgent == agentID {
			return card
		}
		if assignedUser == "" && assignedAgent == "" {
			return card
		}
	}
	return nil
}

func formatTaskCardPrompt(content string, sm shadowMessage, card map[string]any) string {
	title := strings.TrimSpace(stringValue(card["title"]))
	if title == "" {
		title = "Inbox task"
	}
	body := strings.TrimSpace(stringValue(card["body"]))
	cardID := taskCardID(card)
	threadID := taskThreadIDFromCard(card)
	lines := []string{"[Shadow Inbox task]", "Title: " + title}
	if sm.ID != "" {
		lines = append(lines, "Task message id: "+sm.ID)
	}
	if cardID != "" {
		lines = append(lines, "Task card id: "+cardID)
	}
	if threadID != "" {
		lines = append(lines, "Task thread id: "+threadID)
	}
	if priority := strings.TrimSpace(stringValue(card["priority"])); priority != "" {
		lines = append(lines, "Priority: "+priority)
	}
	lines = append(lines,
		"",
		"Send ordinary task discussion replies to the Shadow task thread.",
		"Do not change the task status unless the human explicitly asks you to update it.",
	)
	if body != "" {
		lines = append(lines, "", body)
	}
	if trimmed := strings.TrimSpace(content); trimmed != "" && trimmed != title && trimmed != body {
		lines = append(lines, "", "Original message:", trimmed)
	}
	return strings.Join(lines, "\n")
}

func formatTaskThreadPrompt(content string, binding taskThreadBinding) string {
	title := strings.TrimSpace(binding.title)
	if title == "" {
		title = "Inbox task"
	}
	lines := []string{
		"[Shadow Inbox task thread comment]",
		"Task title: " + title,
		"Task message id: " + binding.messageID,
		"Task card id: " + binding.cardID,
		"Reply as an ordinary discussion message in this same task thread.",
		"Do not change the task status unless the human explicitly asks you to update it.",
	}
	if trimmed := strings.TrimSpace(content); trimmed != "" {
		lines = append(lines, "", trimmed)
	}
	return strings.Join(lines, "\n")
}

func taskSessionKey(channelID, threadID, messageID, cardID string) string {
	parts := []string{"shadowob", "task", "channel", channelID}
	if threadID != "" {
		parts = append(parts, "thread", threadID)
	}
	parts = append(parts, "message", messageID, "card", cardID)
	return strings.Join(parts, ":")
}

func (p *Platform) rememberTaskThreadBinding(binding taskThreadBinding) {
	if binding.threadID == "" || binding.messageID == "" || binding.cardID == "" {
		return
	}
	p.mu.Lock()
	if p.taskThreadBindings == nil {
		p.taskThreadBindings = make(map[string]taskThreadBinding)
	}
	p.taskThreadBindings[binding.threadID] = binding
	p.mu.Unlock()
}

func (p *Platform) taskThreadBinding(threadID string) (taskThreadBinding, bool) {
	p.mu.RLock()
	binding, ok := p.taskThreadBindings[threadID]
	p.mu.RUnlock()
	return binding, ok
}

func (p *Platform) activateTaskCard(ctx context.Context, sm shadowMessage, card map[string]any) (shadowMessage, map[string]any, bool) {
	cardID := taskCardID(card)
	if cardID == "" {
		return sm, card, true
	}
	status := strings.ToLower(strings.TrimSpace(stringValue(card["status"])))
	current := sm
	if status == "" || status == "queued" {
		reqCtx, cancel := requestContext(ctx)
		updated, err := p.client.claimTaskCard(reqCtx, sm.ID, cardID, "cc-connect accepted the Inbox task.")
		cancel()
		if err != nil {
			slog.Debug("shadowob: claim task card failed", "message_id", sm.ID, "card_id", cardID, "error", err)
			return sm, card, false
		}
		if updated != nil {
			current = *updated
			if next := taskCardByID(current, cardID); next != nil {
				card = next
				status = strings.ToLower(strings.TrimSpace(stringValue(card["status"])))
			}
		}
	}
	if status == "queued" || status == "claimed" {
		reqCtx, cancel := requestContext(ctx)
		updated, err := p.client.updateTaskCard(reqCtx, sm.ID, cardID, "running", "cc-connect started working on the task.")
		cancel()
		if err != nil {
			slog.Debug("shadowob: update task card running failed", "message_id", sm.ID, "card_id", cardID, "error", err)
			return current, card, false
		}
		if updated != nil {
			current = *updated
			if next := taskCardByID(current, cardID); next != nil {
				card = next
			}
		}
	}
	return current, card, true
}

func taskCardByID(sm shadowMessage, cardID string) map[string]any {
	if len(sm.Metadata) == 0 || cardID == "" {
		return nil
	}
	cards, _ := sm.Metadata["cards"].([]any)
	for _, raw := range cards {
		card, ok := raw.(map[string]any)
		if ok && card["kind"] == "task" && taskCardID(card) == cardID {
			return card
		}
	}
	return nil
}

func (p *Platform) handleChannelMessage(ctx context.Context, sm shadowMessage) {
	p.mu.RLock()
	rt, known := p.channels[sm.ChannelID]
	buddyUserID := p.me.ID
	p.mu.RUnlock()
	if !known && len(p.configChannelIDs) > 0 {
		return
	}
	if p.shouldSkipMessage(sm) {
		return
	}
	if !p.allowedSender(sm) {
		return
	}
	mentionsMe := p.messageMentionsMe(sm)
	taskCard := runtimeTaskCardForSelf(sm, buddyUserID, p.agentID)
	var taskBinding *taskThreadBinding
	if taskCard != nil {
		updated, nextCard, ok := p.activateTaskCard(ctx, sm, taskCard)
		if !ok {
			return
		}
		sm = updated
		taskCard = nextCard
		cardID := taskCardID(taskCard)
		threadID := taskThreadIDFromCard(taskCard)
		binding := taskThreadBinding{
			channelID: sm.ChannelID,
			threadID:  threadID,
			messageID: sm.ID,
			cardID:    cardID,
			title:     strings.TrimSpace(stringValue(taskCard["title"])),
		}
		if threadID != "" {
			p.rememberTaskThreadBinding(binding)
		}
		taskBinding = &binding
	} else if sm.ThreadID != "" {
		if binding, ok := p.taskThreadBinding(sm.ThreadID); ok {
			taskBinding = &binding
		}
	}
	hasTaskContext := taskCard != nil || taskBinding != nil
	if rt.Policy.Listen && rt.Policy.Reply == false && !mentionsMe && !hasTaskContext {
		slog.Debug("shadowob: ignoring no-reply policy message", "channel_id", sm.ChannelID, "message_id", sm.ID)
		return
	}
	if rt.Policy.MentionOnly && !mentionsMe && !hasTaskContext && sm.ThreadID == "" {
		slog.Debug("shadowob: ignoring unmentioned message", "channel_id", sm.ChannelID, "message_id", sm.ID)
		return
	}

	if sm.ThreadID != "" && mentionsMe && !hasTaskContext {
		confirmed, ok := p.confirmPersistedThreadMention(ctx, sm)
		if !ok {
			return
		}
		sm = confirmed
		mentionsMe = p.messageMentionsMe(sm)
		if !mentionsMe {
			slog.Debug("shadowob: ignoring persisted thread message without explicit mention", "channel_id", sm.ChannelID, "message_id", sm.ID)
			return
		}
	}

	isAuthorBuddy := messageAuthorIsBuddy(sm)
	var threadBuddyDiscussion *buddyThreadDiscussionState
	if isAuthorBuddy && !hasTaskContext {
		replyToBuddy := policyConfigBool(rt.Policy.Config, "replyToBuddy", false)
		if !replyToBuddy && sm.ThreadID == "" {
			slog.Debug("shadowob: ignoring Buddy main-channel message because replyToBuddy=false", "channel_id", sm.ChannelID, "message_id", sm.ID)
			return
		}
		if sm.ThreadID != "" {
			if !mentionsMe {
				slog.Debug("shadowob: ignoring Buddy thread message without explicit mention", "channel_id", sm.ChannelID, "message_id", sm.ID)
				return
			}
			var allowed bool
			threadBuddyDiscussion, allowed = nextBuddyDiscussionState(sm, buddyUserID)
			if !allowed {
				slog.Debug("shadowob: ignoring Buddy thread follow-up beyond discussion turn limit", "channel_id", sm.ChannelID, "message_id", sm.ID)
				return
			}
		}
		if !senderBuddyAllowed(rt.Policy.Config, sm) {
			slog.Debug("shadowob: ignoring Buddy message because buddy allowlist policy denied it", "channel_id", sm.ChannelID, "message_id", sm.ID)
			return
		}
	}
	if !isAuthorBuddy && messageMentionsAnyBuddy(sm) && !mentionsMe && !hasTaskContext {
		slog.Debug("shadowob: ignoring message that targets other Buddies", "channel_id", sm.ChannelID, "message_id", sm.ID)
		return
	}

	coordination, ok := p.coordinateBuddyThread(ctx, sm, rt.Policy.Config)
	if !ok {
		return
	}
	if coordination != nil {
		sm.ThreadID = coordination.threadID
	}

	if p.handleLocalSlashPrompt(ctx, sm, false) {
		return
	}
	if taskCard != nil {
		sm.Content = formatTaskCardPrompt(sm.Content, sm, taskCard)
	} else if taskBinding != nil {
		sm.Content = formatTaskThreadPrompt(sm.Content, *taskBinding)
	}
	threadBuddyFollowup := isAuthorBuddy && sm.ThreadID != "" && mentionsMe && !hasTaskContext
	msg := p.toCoreMessage(ctx, sm, false, rt, coordination, taskBinding, threadBuddyFollowup, threadBuddyDiscussion)
	if msg == nil {
		return
	}
	p.dispatch(msg)
}

func (p *Platform) handleDMMessage(ctx context.Context, sm shadowMessage) {
	if sm.DMChannelID == "" {
		sm.DMChannelID = sm.ChannelID
	}
	if sm.DMChannelID == "" {
		return
	}
	if p.shouldSkipMessage(sm) || !p.allowedSender(sm) {
		return
	}
	p.addDM(sm.DMChannelID)
	if p.handleLocalSlashPrompt(ctx, sm, true) {
		return
	}
	msg := p.toCoreMessage(ctx, sm, true, channelRuntime{}, nil, nil, false, nil)
	if msg == nil {
		return
	}
	p.dispatch(msg)
}

func (p *Platform) handleLocalSlashPrompt(ctx context.Context, sm shadowMessage, dm bool) bool {
	content := strings.TrimSpace(sm.Content)
	if len(p.localCommands) == 0 || content == "" || content[0] != '/' || interactiveResponse(sm.Metadata) != nil {
		return false
	}
	match := matchSlashCommand(content, p.localCommands)
	if match == nil || match.Command.Interaction == nil || strings.TrimSpace(match.Args) != "" {
		return false
	}
	block := *match.Command.Interaction
	block.ID = firstNonEmpty(block.ID, "slash:"+match.Command.Name+":"+sm.ID)
	contentToSend := firstNonEmpty(block.Prompt, fmt.Sprintf("/%s needs input before cc-connect can continue.", match.Command.Name))
	metadata := p.deliveryMetadata(map[string]any{
		"interactive": block,
		"slashCommand": map[string]any{
			"name":        match.Command.Name,
			"invokedName": match.InvokedName,
			"description": match.Command.Description,
			"args":        match.Args,
			"packId":      match.Command.PackID,
		},
	})
	rc := p.replyContextForMessage(sm, dm, nil)
	_, err := p.sendToReplyContext(ctx, rc, contentToSend, true, metadata)
	if err != nil {
		slog.Warn("shadowob: send slash form failed", "error", err)
	}
	return true
}

func (p *Platform) toCoreMessage(ctx context.Context, sm shadowMessage, dm bool, rt channelRuntime, coordination *buddyThreadCoordination, taskBinding *taskThreadBinding, threadBuddyFollowup bool, threadBuddyDiscussion *buddyThreadDiscussionState) *core.Message {
	body := sm.Content
	if ir := interactiveResponse(sm.Metadata); ir != nil {
		body = p.interactiveResponseContent(ctx, sm, ir)
	} else if match := matchSlashCommand(body, p.localCommands); match != nil {
		body = formatSlashCommandPrompt(body, match)
	}
	if coordination != nil {
		body = stripBuddyMentionTokens(body, coordination.buddyUserIDs)
	} else if threadBuddyFollowup {
		body = stripBuddyMentionTokens(body, []string{p.me.ID})
	}

	images, files, audio, cleanBody := p.resolveInboundMedia(ctx, sm, body)
	if cleanBody != "" {
		body = cleanBody
	}

	authorID := messageAuthorID(sm)
	authorName := messageAuthorName(sm)
	if authorName == "" {
		authorName = authorID
	}
	effectiveThreadID := sm.ThreadID
	if taskBinding != nil && taskBinding.threadID != "" {
		effectiveThreadID = taskBinding.threadID
	}
	sessionKey := p.sessionKeyFor(sm, dm, authorID, effectiveThreadID)
	if taskBinding != nil {
		sessionKey = taskSessionKey(taskBinding.channelID, taskBinding.threadID, taskBinding.messageID, taskBinding.cardID)
	}
	channelKey := "shadowob:channel:" + sm.ChannelID
	if dm {
		channelKey = "shadowob:dm:" + sm.DMChannelID
	} else if effectiveThreadID != "" {
		channelKey = "shadowob:channel:" + sm.ChannelID + ":thread:" + effectiveThreadID
	}

	chatName := ""
	if dm {
		chatName = "Shadow DM"
	} else if rt.Name != "" {
		chatName = "#" + rt.Name
	}
	rc := p.replyContextForMessage(sm, dm, taskBinding)
	rc.discussion = outboundBuddyDiscussionState(coordination, threadBuddyDiscussion)
	return &core.Message{
		SessionKey:       sessionKey,
		Platform:         "shadowob",
		MessageID:        sm.ID,
		UserID:           authorID,
		UserName:         authorName,
		ChatName:         chatName,
		Content:          body,
		Images:           images,
		Files:            files,
		Audio:            audio,
		ChannelKey:       channelKey,
		ExtraContent:     formatShadowExtraPrompt(coordination, threadBuddyFollowup, threadBuddyDiscussion, p.me.ID),
		ReplyCtx:         rc,
		SuppressQueueAck: threadBuddyFollowup,
	}
}

func formatShadowExtraPrompt(coordination *buddyThreadCoordination, threadBuddyFollowup bool, threadBuddyDiscussion *buddyThreadDiscussionState, meID string) string {
	parts := []string{}
	if prompt := formatBuddyThreadCoordinationPrompt(coordination); prompt != "" {
		parts = append(parts, prompt)
	}
	if threadBuddyFollowup {
		parts = append(parts, formatBuddyThreadFollowupPrompt(threadBuddyDiscussion, meID))
	}
	return strings.Join(parts, "\n\n")
}

func outboundBuddyDiscussionState(coordination *buddyThreadCoordination, followup *buddyThreadDiscussionState) *buddyThreadDiscussionState {
	if followup != nil {
		return followup
	}
	if coordination != nil {
		return coordination.discussion
	}
	return nil
}

func (p *Platform) replyContextForMessage(sm shadowMessage, dm bool, taskBinding *taskThreadBinding) replyContext {
	authorID := messageAuthorID(sm)
	threadID := sm.ThreadID
	replyToID := sm.ID
	if taskBinding != nil {
		threadID = taskBinding.threadID
		replyToID = taskBinding.messageID
	}
	sessionKey := p.sessionKeyFor(sm, dm, authorID, threadID)
	taskMessageID := ""
	taskCardID := ""
	taskComplete := false
	if taskBinding != nil {
		sessionKey = taskSessionKey(taskBinding.channelID, taskBinding.threadID, taskBinding.messageID, taskBinding.cardID)
		taskMessageID = taskBinding.messageID
		taskCardID = taskBinding.cardID
		taskComplete = sm.ID == taskBinding.messageID && sm.ThreadID == ""
	}
	return replyContext{
		channelID:     sm.ChannelID,
		dmChannelID:   sm.DMChannelID,
		threadID:      threadID,
		messageID:     sm.ID,
		replyToID:     replyToID,
		sessionKey:    sessionKey,
		taskMessageID: taskMessageID,
		taskCardID:    taskCardID,
		taskComplete:  taskComplete,
	}
}

func (p *Platform) sessionKey(sm shadowMessage, dm bool, authorID string) string {
	return p.sessionKeyFor(sm, dm, authorID, sm.ThreadID)
}

func (p *Platform) sessionKeyFor(sm shadowMessage, dm bool, authorID string, threadID string) string {
	if dm {
		if p.shareSessionInChannel {
			return "shadowob:dm:" + sm.DMChannelID
		}
		return "shadowob:dm:" + sm.DMChannelID + ":" + authorID
	}
	conv := sm.ChannelID
	if threadID != "" {
		conv = sm.ChannelID + ":thread:" + threadID
	}
	if p.shareSessionInChannel {
		return "shadowob:channel:" + conv
	}
	return "shadowob:channel:" + conv + ":" + authorID
}

func (p *Platform) dispatch(msg *core.Message) {
	p.mu.RLock()
	handler := p.handler
	p.mu.RUnlock()
	if handler != nil {
		handler(p, msg)
	}
}

func (p *Platform) shouldSkipMessage(sm shadowMessage) bool {
	if authorID := messageAuthorID(sm); authorID != "" {
		p.mu.RLock()
		meID := p.me.ID
		p.mu.RUnlock()
		if meID != "" && authorID == meID {
			return true
		}
	}
	if id := deliveryID(sm.Metadata); id != "" {
		p.mu.Lock()
		p.sweepDeliveriesLocked()
		_, ok := p.sentDeliveryIDs[id]
		p.mu.Unlock()
		if ok {
			return true
		}
	}
	if sm.ID != "" {
		p.mu.Lock()
		p.sweepSentMessagesLocked()
		if _, ok := p.sentMessageIDs[sm.ID]; ok {
			p.mu.Unlock()
			return true
		}
		p.sweepReceivedLocked()
		if _, ok := p.receivedMsgIDs[sm.ID]; ok {
			p.mu.Unlock()
			return true
		}
		p.receivedMsgIDs[sm.ID] = time.Now()
		p.mu.Unlock()
	}
	return false
}

func (p *Platform) confirmPersistedThreadMention(ctx context.Context, sm shadowMessage) (shadowMessage, bool) {
	if p.client == nil || sm.ID == "" {
		return sm, true
	}
	reqCtx, cancel := requestContext(ctx)
	persisted, err := p.client.getMessage(reqCtx, sm.ID)
	cancel()
	if err != nil {
		slog.Debug("shadowob: ignoring transient thread mention that is not readable via REST", "channel_id", sm.ChannelID, "thread_id", sm.ThreadID, "message_id", sm.ID, "error", err)
		return sm, false
	}
	if persisted == nil || persisted.ID == "" {
		slog.Debug("shadowob: ignoring transient thread mention with empty REST payload", "channel_id", sm.ChannelID, "thread_id", sm.ThreadID, "message_id", sm.ID)
		return sm, false
	}
	return *persisted, true
}

func (p *Platform) allowedSender(sm shadowMessage) bool {
	authorID := messageAuthorID(sm)
	username := ""
	if sm.Author != nil {
		username = sm.Author.Username
	}
	return core.AllowList(p.allowFrom, authorID) || (username != "" && core.AllowList(p.allowFrom, username))
}

func (p *Platform) messageMentionsMe(sm shadowMessage) bool {
	p.mu.RLock()
	me := p.me
	p.mu.RUnlock()
	if me.ID == "" && me.Username == "" {
		return false
	}
	if strings.Contains(strings.ToLower(sm.Content), "@"+strings.ToLower(me.Username)) {
		return true
	}
	mentions, _ := sm.Metadata["mentions"].([]any)
	for _, mention := range mentions {
		m, ok := mention.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(m["userId"]) == me.ID || strings.EqualFold(stringValue(m["username"]), me.Username) {
			return true
		}
	}
	return false
}

func (p *Platform) interactiveResponseContent(ctx context.Context, sm shadowMessage, response map[string]any) string {
	value := firstNonEmpty(stringValue(response["value"]), stringValue(response["actionId"]))
	switch value {
	case "perm:allow":
		return "allow"
	case "perm:deny":
		return "deny"
	case "perm:allow_all":
		return "allow all"
	}
	if strings.HasPrefix(value, "cmd:") {
		return strings.TrimPrefix(value, "cmd:")
	}
	if strings.HasPrefix(value, "askq:") {
		return value
	}

	var source *shadowMessage
	if sourceID := stringValue(response["sourceMessageId"]); sourceID != "" {
		reqCtx, cancel := requestContext(ctx)
		source, _ = p.client.getMessage(reqCtx, sourceID)
		cancel()
	}
	lines := []string{
		"Shadow interactive response received.",
		"Use the submitted values once. Do not separately restate or grade the submitted form unless the source command explicitly asks for an evaluation.",
		"If the next step is another Shadow interactive dialog, send that dialog only and do not add a separate normal text reply for the same step.",
	}
	if source != nil && source.Content != "" {
		lines = append(lines, "Source message: "+source.Content)
	}
	if source != nil {
		if sc, ok := source.Metadata["slashCommand"].(map[string]any); ok {
			if body := stringValue(sc["body"]); body != "" {
				lines = append(lines, "Source slash command definition:\n"+body)
			} else if name := stringValue(sc["name"]); name != "" {
				if body := p.localSlashCommandBody(name); body != "" {
					lines = append(lines, "Source slash command definition:\n"+body)
				}
			}
		}
	}
	if action := stringValue(response["actionId"]); action != "" {
		lines = append(lines, "Action: "+action)
	}
	if values, ok := response["values"]; ok {
		if data, err := json.MarshalIndent(values, "", "  "); err == nil {
			lines = append(lines, "Submitted values:\n"+string(data))
		}
	} else if value != "" {
		lines = append(lines, "Value: "+value)
	}
	return strings.Join(lines, "\n\n")
}

func (p *Platform) resolveInboundMedia(ctx context.Context, sm shadowMessage, body string) ([]core.ImageAttachment, []core.FileAttachment, *core.AudioAttachment, string) {
	urls := make([]shadowAttachment, 0, len(sm.Attachments))
	seen := map[string]bool{}
	for _, a := range sm.Attachments {
		if a.URL == "" || seen[a.URL] {
			continue
		}
		seen[a.URL] = true
		urls = append(urls, a)
	}
	for _, m := range markdownMediaRE.FindAllStringSubmatch(body, -1) {
		if len(m) < 2 || m[1] == "" || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		urls = append(urls, shadowAttachment{URL: m[1]})
	}
	if len(urls) == 0 {
		return nil, nil, nil, body
	}

	var images []core.ImageAttachment
	var files []core.FileAttachment
	var audio *core.AudioAttachment
	dm := sm.DMChannelID != ""
	for _, ref := range urls {
		if ref.Size > p.mediaMaxBytes {
			slog.Warn("shadowob: skipping large attachment", "url", ref.URL, "size", ref.Size)
			continue
		}
		reqCtx, cancel := requestContext(ctx)
		downloadURL := ref.URL
		if ref.ID != "" {
			if signedURL, err := p.client.resolveAttachmentMediaURL(reqCtx, ref.ID, dm); err == nil {
				downloadURL = signedURL
			} else {
				slog.Debug("shadowob: resolve signed media url failed", "attachment_id", ref.ID, "error", err)
			}
		}
		data, ct, filename, err := p.client.downloadFile(reqCtx, downloadURL, p.mediaMaxBytes)
		cancel()
		if err != nil {
			slog.Warn("shadowob: download attachment failed", "url", ref.URL, "error", err)
			continue
		}
		if int64(len(data)) > p.mediaMaxBytes {
			slog.Warn("shadowob: skipping downloaded large attachment", "url", ref.URL, "size", len(data))
			continue
		}
		if ref.ContentType != "" {
			ct = ref.ContentType
		}
		if ref.Filename != "" {
			filename = ref.Filename
		}
		ct = inferMime(filename, ct)
		switch {
		case strings.HasPrefix(ct, "image/"):
			images = append(images, core.ImageAttachment{MimeType: ct, Data: data, FileName: filename})
		case strings.HasPrefix(ct, "audio/") && audio == nil:
			audio = &core.AudioAttachment{MimeType: ct, Data: data, Format: audioFormat(filename, ct)}
		default:
			files = append(files, core.FileAttachment{MimeType: ct, Data: data, FileName: filename})
		}
	}
	if len(images) == 0 && len(files) == 0 && audio == nil {
		return nil, nil, nil, body
	}
	cleanBody := strings.TrimSpace(markdownMediaRE.ReplaceAllString(body, ""))
	if cleanBody == "" {
		cleanBody = "[Media attached]"
	}
	return images, files, audio, cleanBody
}

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("shadowob: invalid reply context type %T", replyCtx)
	}
	_, err := p.sendToReplyContext(ctx, rc, content, true, nil)
	if err == nil {
		p.completeTaskAfterReply(ctx, rc)
	}
	return err
}

func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("shadowob: invalid reply context type %T", replyCtx)
	}
	if previewID := p.popPreviewID(rc.sessionKey); previewID != "" {
		reqCtx, cancel := requestContext(ctx)
		_, err := p.client.editMessage(reqCtx, previewID, content)
		cancel()
		if err == nil {
			p.completeTaskAfterReply(ctx, rc)
		}
		return err
	}
	_, err := p.sendToReplyContext(ctx, rc, content, false, nil)
	if err == nil {
		p.completeTaskAfterReply(ctx, rc)
	}
	return err
}

func (p *Platform) completeTaskAfterReply(ctx context.Context, rc replyContext) {
	if !rc.taskComplete || rc.taskMessageID == "" || rc.taskCardID == "" || p.client == nil {
		return
	}
	reqCtx, cancel := requestContext(ctx)
	defer cancel()
	if _, err := p.client.updateTaskCard(reqCtx, rc.taskMessageID, rc.taskCardID, "completed", "cc-connect completed the task."); err != nil {
		slog.Warn("shadowob: complete task card after reply failed", "message_id", rc.taskMessageID, "card_id", rc.taskCardID, "error", err)
	}
}

func (p *Platform) popPreviewID(sessionKey string) string {
	if sessionKey == "" {
		return ""
	}
	p.mu.Lock()
	id := p.previewMsgs[sessionKey]
	delete(p.previewMsgs, sessionKey)
	p.mu.Unlock()
	return id
}

func (p *Platform) SendWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]core.ButtonOption) error {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("shadowob: invalid reply context type %T", replyCtx)
	}
	block := shadowInteractiveBlock{
		ID:     "cc_buttons_" + randomID(),
		Kind:   "buttons",
		Prompt: content,
	}
	for _, row := range buttons {
		for _, button := range row {
			if button.Text == "" || button.Data == "" {
				continue
			}
			block.Buttons = append(block.Buttons, shadowInteractiveItem{
				ID:    truncateString(button.Data, 80),
				Label: truncateString(button.Text, 120),
				Value: button.Data,
			})
		}
	}
	if len(block.Buttons) == 0 {
		return p.Send(ctx, replyCtx, content)
	}
	_, err := p.sendToReplyContext(ctx, rc, content, false, p.deliveryMetadata(map[string]any{"interactive": block}))
	return err
}

func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("shadowob: invalid reply context type %T", replyCtx)
	}
	name := firstNonEmpty(img.FileName, "image.png")
	mimeType := firstNonEmpty(img.MimeType, inferMime(name, "image/png"))
	return p.sendAttachment(ctx, rc, img.Data, name, mimeType)
}

func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("shadowob: invalid reply context type %T", replyCtx)
	}
	name := firstNonEmpty(file.FileName, "attachment")
	mimeType := firstNonEmpty(file.MimeType, inferMime(name, "application/octet-stream"))
	return p.sendAttachment(ctx, rc, file.Data, name, mimeType)
}

func (p *Platform) sendAttachment(ctx context.Context, rc replyContext, data []byte, filename, contentType string) error {
	reqCtx, cancel := requestContext(ctx)
	upload, err := p.client.uploadMedia(reqCtx, data, filename, contentType, "", "")
	cancel()
	if err != nil {
		return fmt.Errorf("shadowob: upload media: %w", err)
	}

	attachment := map[string]any{
		"filename":    filename,
		"url":         upload.URL,
		"contentType": contentType,
		"size":        upload.Size,
	}

	content := "\u200B"
	metadata := p.deliveryMetadata(nil)
	p.mu.Lock()
	if id := deliveryID(metadata); id != "" {
		p.sentDeliveryIDs[id] = time.Now()
		p.sweepDeliveriesLocked()
	}
	p.mu.Unlock()

	sendCtx, sendCancel := requestContext(ctx)
	defer sendCancel()
	var msg *shadowMessage
	replyToID := firstNonEmpty(rc.replyToID, rc.messageID)
	if rc.dmChannelID != "" {
		msg, err = p.client.sendDMMessage(sendCtx, rc.dmChannelID, content, sendMessageOptions{
			ReplyToID:   replyToID,
			Metadata:    metadata,
			Attachments: []any{attachment},
		})
	} else {
		if rc.channelID == "" {
			return fmt.Errorf("shadowob: empty channel target")
		}
		msg, err = p.client.sendMessage(sendCtx, rc.channelID, content, sendMessageOptions{
			ThreadID:    rc.threadID,
			ReplyToID:   replyToID,
			Metadata:    metadata,
			Attachments: []any{attachment},
		})
	}
	if err != nil {
		return fmt.Errorf("shadowob: send attachment: %w", err)
	}
	p.recordSentMessageID(msg)
	return nil
}

func (p *Platform) SendPreviewStart(ctx context.Context, replyCtx any, content string) (any, error) {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("shadowob: invalid reply context type %T", replyCtx)
	}
	msg, err := p.sendToReplyContext(ctx, rc, content, false, nil)
	if err != nil {
		return nil, err
	}
	if rc.sessionKey != "" {
		p.mu.Lock()
		p.previewMsgs[rc.sessionKey] = msg.ID
		p.mu.Unlock()
	}
	return previewHandle{messageID: msg.ID}, nil
}

func (p *Platform) UpdateMessage(ctx context.Context, replyCtx any, content string) error {
	messageID := ""
	switch v := replyCtx.(type) {
	case previewHandle:
		messageID = v.messageID
	case *previewHandle:
		if v != nil {
			messageID = v.messageID
		}
	case replyContext:
		messageID = v.messageID
	default:
		return fmt.Errorf("shadowob: invalid update handle type %T", replyCtx)
	}
	if messageID == "" {
		return fmt.Errorf("shadowob: empty update message id")
	}
	reqCtx, cancel := requestContext(ctx)
	_, err := p.client.editMessage(reqCtx, messageID, content)
	cancel()
	if err != nil {
		return fmt.Errorf("shadowob: update message: %w", err)
	}
	return nil
}

func (p *Platform) DeletePreviewMessage(ctx context.Context, preview any) error {
	handle, ok := preview.(previewHandle)
	if !ok {
		if ptr, ok := preview.(*previewHandle); ok && ptr != nil {
			handle = *ptr
		} else {
			return fmt.Errorf("shadowob: invalid preview handle type %T", preview)
		}
	}
	if handle.messageID == "" {
		return nil
	}
	p.mu.Lock()
	for k, v := range p.previewMsgs {
		if v == handle.messageID {
			delete(p.previewMsgs, k)
		}
	}
	p.mu.Unlock()
	reqCtx, cancel := requestContext(ctx)
	err := p.client.deleteMessage(reqCtx, handle.messageID)
	cancel()
	return err
}

func (p *Platform) StartTyping(ctx context.Context, replyCtx any) func() {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return func() {}
	}
	stop := make(chan struct{})
	send := func(typing bool) {
		p.mu.RLock()
		socket := p.socket
		p.mu.RUnlock()
		if socket == nil {
			return
		}
		if rc.dmChannelID != "" {
			_ = socket.emit("dm:typing", map[string]any{"dmChannelId": rc.dmChannelID, "typing": typing})
			return
		}
		if rc.channelID != "" {
			_ = socket.emit("message:typing", map[string]any{"channelId": rc.channelID, "typing": typing})
		}
	}
	send(true)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				send(false)
				return
			case <-stop:
				send(false)
				return
			case <-t.C:
				send(true)
			}
		}
	}()
	return func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
	}
}

func (p *Platform) ProgressStyle() string { return p.progressStyle }

func (p *Platform) FormattingInstructions() string {
	return "Shadow supports standard Markdown. " +
		"Use **bold** and *italic* for emphasis. " +
		"Code blocks with language hint are preferred. " +
		"Tables should be kept under 8 columns and 30 rows for readability."
}

func (p *Platform) SetLifecycleHandler(h core.PlatformLifecycleHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lifecycleHandler = h
}

func (p *Platform) notifyReady(gen uint64, socket *socketClient) {
	p.mu.RLock()
	match := p.socket == socket && p.generation == gen
	handler := p.lifecycleHandler
	p.mu.RUnlock()
	if match && handler != nil {
		handler.OnPlatformReady(p)
	}
}

func (p *Platform) notifyUnavailable(err error) {
	p.mu.Lock()
	if p.unavailableNotified || err == nil {
		p.mu.Unlock()
		return
	}
	p.unavailableNotified = true
	handler := p.lifecycleHandler
	p.mu.Unlock()
	if handler != nil {
		handler.OnPlatformUnavailable(p, err)
	}
}

func (p *Platform) RegisterCommands(commands []core.BotCommandInfo) error {
	p.mu.Lock()
	p.coreCommands = commandsFromCore(commands)
	p.mu.Unlock()
	return p.registerSlashCommands(context.Background())
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	parts := strings.Split(sessionKey, ":")
	if len(parts) < 3 || parts[0] != "shadowob" {
		return nil, fmt.Errorf("shadowob: invalid session key %q", sessionKey)
	}
	switch parts[1] {
	case "dm":
		return replyContext{dmChannelID: parts[2], sessionKey: sessionKey}, nil
	case "channel":
		rc := replyContext{channelID: parts[2], sessionKey: sessionKey}
		if len(parts) >= 5 && parts[3] == "thread" {
			rc.threadID = parts[4]
		}
		return rc, nil
	case "task":
		return p.reconstructTaskReplyCtx(sessionKey, parts[2:])
	default:
		return nil, fmt.Errorf("shadowob: invalid session target %q", parts[1])
	}
}

func (p *Platform) reconstructTaskReplyCtx(sessionKey string, parts []string) (replyContext, error) {
	if rc, ok := parseExplicitTaskReplyCtx(sessionKey, parts); ok {
		return rc, nil
	}

	messageID, cardID := parseTaskMessageCardIDs(parts)
	if messageID == "" {
		return replyContext{}, fmt.Errorf("shadowob: invalid task session key %q", sessionKey)
	}

	client := p.client
	if client == nil {
		client = newShadowClient(p.serverURL, p.token)
	}
	reqCtx, cancel := requestContext(context.Background())
	message, err := client.getMessage(reqCtx, messageID)
	cancel()
	if err != nil {
		return replyContext{}, fmt.Errorf("shadowob: resolve task message %q: %w", messageID, err)
	}
	if message.ChannelID == "" && message.DMChannelID == "" {
		return replyContext{}, fmt.Errorf("shadowob: task message %q has no reply target", messageID)
	}

	threadID := firstNonEmpty(message.ThreadID, taskThreadIDFromMessage(message, cardID))
	return replyContext{
		channelID:   message.ChannelID,
		dmChannelID: message.DMChannelID,
		threadID:    threadID,
		messageID:   message.ID,
		replyToID:   message.ID,
		sessionKey:  sessionKey,
	}, nil
}

func parseExplicitTaskReplyCtx(sessionKey string, parts []string) (replyContext, bool) {
	fields := taskSessionFields(parts)
	channelID := fields["channel"]
	threadID := fields["thread"]
	messageID := firstNonEmpty(fields["message"], fields["root"])
	if channelID == "" || threadID == "" || messageID == "" {
		return replyContext{}, false
	}
	return replyContext{
		channelID:  channelID,
		threadID:   threadID,
		messageID:  messageID,
		replyToID:  messageID,
		sessionKey: sessionKey,
	}, true
}

func taskSessionFields(parts []string) map[string]string {
	fields := map[string]string{}
	for i := 0; i+1 < len(parts); i += 2 {
		switch parts[i] {
		case "channel", "thread", "message", "root", "card":
			fields[parts[i]] = parts[i+1]
		default:
			return fields
		}
	}
	return fields
}

func parseTaskMessageCardIDs(parts []string) (messageID string, cardID string) {
	fields := taskSessionFields(parts)
	if fields["message"] != "" || fields["card"] != "" {
		return fields["message"], fields["card"]
	}
	switch len(parts) {
	case 2:
		return parts[0], parts[1]
	case 3:
		return parts[1], parts[2]
	default:
		return "", ""
	}
}

func taskThreadIDFromMessage(message *shadowMessage, cardID string) string {
	if message == nil || len(message.Metadata) == 0 {
		return ""
	}
	cards, ok := message.Metadata["cards"].([]any)
	if !ok {
		return ""
	}
	for _, raw := range cards {
		card, ok := raw.(map[string]any)
		if !ok || card["kind"] != "task" {
			continue
		}
		if cardID != "" && card["id"] != cardID {
			continue
		}
		if threadID := stringMapValue(card, "threadId", "taskThreadId"); threadID != "" {
			return threadID
		}
		for _, key := range []string{"data", "target"} {
			if nested, ok := card[key].(map[string]any); ok {
				if threadID := stringMapValue(nested, "threadId", "taskThreadId"); threadID != "" {
					return threadID
				}
				if task, ok := nested["task"].(map[string]any); ok {
					if threadID := stringMapValue(task, "threadId", "thread_id", "taskThreadId"); threadID != "" {
						return threadID
					}
				}
			}
		}
	}
	return ""
}

func stringMapValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (p *Platform) sendToReplyContext(ctx context.Context, rc replyContext, content string, reply bool, metadata map[string]any) (*shadowMessage, error) {
	content = sanitizeBuddyDiscussionOutboundContent(content, rc.discussion, p.me.ID)
	if strings.TrimSpace(content) == "" {
		content = "\u200B"
	}
	metadata = mergeBuddyDiscussionMetadata(metadata, rc.discussion, p.me.ID)
	if metadata == nil {
		metadata = p.deliveryMetadata(nil)
	} else if deliveryID(metadata) == "" {
		metadata = p.deliveryMetadata(metadata)
	}
	if id := deliveryID(metadata); id != "" {
		p.mu.Lock()
		p.sentDeliveryIDs[id] = time.Now()
		p.sweepDeliveriesLocked()
		p.mu.Unlock()
	}
	replyToID := ""
	if reply {
		replyToID = firstNonEmpty(rc.replyToID, rc.messageID)
	}
	reqCtx, cancel := requestContext(ctx)
	defer cancel()
	var msg *shadowMessage
	var err error
	if rc.dmChannelID != "" {
		msg, err = p.client.sendDMMessage(reqCtx, rc.dmChannelID, content, sendMessageOptions{
			ReplyToID: replyToID,
			Metadata:  metadata,
		})
	} else {
		if rc.channelID == "" {
			return nil, fmt.Errorf("shadowob: empty channel target")
		}
		msg, err = p.client.sendMessage(reqCtx, rc.channelID, content, sendMessageOptions{
			ThreadID:  rc.threadID,
			ReplyToID: replyToID,
			Metadata:  metadata,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("shadowob: send message: %w", err)
	}
	p.recordSentMessageID(msg)
	return msg, nil
}

func mergeBuddyDiscussionMetadata(metadata map[string]any, discussion *buddyThreadDiscussionState, speakerUserID string) map[string]any {
	discussionMetadata := buddyDiscussionMetadata(discussion, speakerUserID)
	if len(discussionMetadata) == 0 {
		return metadata
	}
	out := map[string]any{}
	for k, v := range metadata {
		out[k] = v
	}
	custom := map[string]any{}
	if existing, ok := out["custom"].(map[string]any); ok {
		for k, v := range existing {
			custom[k] = v
		}
	}
	custom[buddyDiscussionMetadataKey] = discussionMetadata
	out["custom"] = custom
	return out
}

func (p *Platform) recordSentMessageID(msg *shadowMessage) {
	if msg == nil || msg.ID == "" {
		return
	}
	p.mu.Lock()
	if p.sentMessageIDs == nil {
		p.sentMessageIDs = make(map[string]time.Time)
	}
	p.sentMessageIDs[msg.ID] = time.Now()
	p.sweepSentMessagesLocked()
	p.mu.Unlock()
}

func (p *Platform) deliveryMetadata(extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range extra {
		if k == "ccConnectDelivery" || k == "shadowDelivery" {
			continue
		}
		out[k] = v
	}
	out["ccConnectDelivery"] = map[string]any{
		"id":     randomID(),
		"source": "cc-connect-shadowob",
	}
	return out
}

func (p *Platform) sweepDeliveriesLocked() {
	if time.Since(p.lastDeliverySweep) < time.Minute {
		return
	}
	p.lastDeliverySweep = time.Now()
	cutoff := time.Now().Add(-10 * time.Minute)
	for id, ts := range p.sentDeliveryIDs {
		if ts.Before(cutoff) {
			delete(p.sentDeliveryIDs, id)
		}
	}
}

func (p *Platform) sweepSentMessagesLocked() {
	if time.Since(p.lastSentMsgSweep) < time.Minute {
		return
	}
	p.lastSentMsgSweep = time.Now()
	cutoff := time.Now().Add(-10 * time.Minute)
	for id, ts := range p.sentMessageIDs {
		if ts.Before(cutoff) {
			delete(p.sentMessageIDs, id)
		}
	}
}

func (p *Platform) sweepReceivedLocked() {
	if time.Since(p.lastReceivedSweep) < time.Minute {
		return
	}
	p.lastReceivedSweep = time.Now()
	cutoff := time.Now().Add(-10 * time.Minute)
	for id, ts := range p.receivedMsgIDs {
		if ts.Before(cutoff) {
			delete(p.receivedMsgIDs, id)
		}
	}
}

func (p *Platform) registerSlashCommands(ctx context.Context) error {
	p.mu.RLock()
	agentID := p.agentID
	commands := append([]shadowSlashCommand{}, p.coreCommands...)
	commands = append(commands, p.localCommands...)
	client := p.client
	p.mu.RUnlock()
	if agentID == "" || client == nil || len(commands) == 0 {
		return nil
	}
	commands = publicSlashCommands(commands)
	reqCtx, cancel := requestContext(ctx)
	err := client.updateAgentSlashCommands(reqCtx, agentID, commands)
	cancel()
	return err
}

func (p *Platform) localSlashCommandBody(name string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, cmd := range p.localCommands {
		if strings.EqualFold(cmd.Name, name) {
			return cmd.Body
		}
		for _, alias := range cmd.Aliases {
			if strings.EqualFold(alias, name) {
				return cmd.Body
			}
		}
	}
	return ""
}

func publicSlashCommands(commands []shadowSlashCommand) []shadowSlashCommand {
	out := make([]shadowSlashCommand, len(commands))
	copy(out, commands)
	for i := range out {
		out[i].Body = ""
	}
	return out
}

func (p *Platform) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		reqCtx, cancel := requestContext(ctx)
		err := p.client.sendHeartbeat(reqCtx, p.agentID)
		cancel()
		if err != nil {
			slog.Debug("shadowob: heartbeat failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func interactiveResponse(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	if m, ok := metadata["interactiveResponse"].(map[string]any); ok {
		return m
	}
	return nil
}

func deliveryID(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if m, ok := metadata["ccConnectDelivery"].(map[string]any); ok {
		return stringValue(m["id"])
	}
	if m, ok := metadata["shadowDelivery"].(map[string]any); ok {
		return stringValue(m["id"])
	}
	return ""
}

func messageAuthorID(sm shadowMessage) string {
	if sm.AuthorID != "" {
		return sm.AuthorID
	}
	if sm.SenderID != "" {
		return sm.SenderID
	}
	if sm.Author != nil {
		return sm.Author.ID
	}
	return ""
}

func messageAuthorName(sm shadowMessage) string {
	if sm.Author == nil {
		return ""
	}
	return firstNonEmpty(sm.Author.DisplayName, sm.Author.Username, sm.Author.ID)
}

func inferMime(filename, header string) string {
	header = strings.TrimSpace(strings.Split(header, ";")[0])
	if header != "" && header != "application/octet-stream" {
		return header
	}
	if ext := filepath.Ext(filename); ext != "" {
		if mt := mime.TypeByExtension(ext); mt != "" {
			return mt
		}
	}
	return firstNonEmpty(header, "application/octet-stream")
}

func audioFormat(filename, mimeType string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), ".")
	if ext != "" {
		return ext
	}
	if parts := strings.SplitN(mimeType, "/", 2); len(parts) == 2 {
		return parts[1]
	}
	return "audio"
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func optionString(opts map[string]any, key, def string) string {
	if opts == nil {
		return def
	}
	if v, ok := opts[key]; ok {
		s := strings.TrimSpace(stringValue(v))
		if s != "" {
			return s
		}
	}
	return def
}

func optionBool(opts map[string]any, key string, def bool) bool {
	if opts == nil {
		return def
	}
	v, ok := opts[key]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	}
	return def
}

func optionInt(opts map[string]any, key string, def int) int {
	if opts == nil {
		return def
	}
	if n, ok := numberInt(opts[key]); ok {
		return n
	}
	return def
}

func optionStringList(opts map[string]any, key string) []string {
	if opts == nil {
		return nil
	}
	v, ok := opts[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return cleanStringList(x)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			out = append(out, stringValue(item))
		}
		return cleanStringList(out)
	case string:
		return cleanStringList(strings.FieldsFunc(x, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n' || r == '\t'
		}))
	default:
		return nil
	}
}

func cleanStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

var getenv = os.Getenv

var _ core.InlineButtonSender = (*Platform)(nil)
var _ core.ImageSender = (*Platform)(nil)
var _ core.FileSender = (*Platform)(nil)
var _ core.MessageUpdater = (*Platform)(nil)
var _ core.PreviewStarter = (*Platform)(nil)
var _ core.PreviewCleaner = (*Platform)(nil)
var _ core.TypingIndicator = (*Platform)(nil)
var _ core.ProgressStyleProvider = (*Platform)(nil)
var _ core.CommandRegistrar = (*Platform)(nil)
var _ core.ReplyContextReconstructor = (*Platform)(nil)
var _ core.FormattingInstructionProvider = (*Platform)(nil)
var _ core.AsyncRecoverablePlatform = (*Platform)(nil)
