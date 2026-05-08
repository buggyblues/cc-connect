package orbit

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type sessionConfig struct {
	socketPath      string
	workDir         string
	fallbackAgent   string
	autoReconnect   bool
	heartbeat       time.Duration
	throttleDelta   time.Duration
	delegateOptions map[string]any
	fallbackOptions map[string]any
	sessionEnv      []string
	platformPrompt  string
}

type Session struct {
	cfg           sessionConfig
	ctx           context.Context
	cancel        context.CancelFunc
	events        chan core.Event
	eventMu       sync.RWMutex
	eventsClosed  bool
	alive         atomic.Bool
	sessionID     atomic.Value // string
	requestSeq    atomic.Uint64
	connMu        sync.Mutex
	conn          net.Conn
	writer        *bufio.Writer
	pendingMu     sync.Mutex
	pending       map[string]*requestState
	deltaMu       sync.Mutex
	deltaBuf      map[string]string
	deltaTimers   map[string]*time.Timer
	delegateMu    sync.Mutex
	delegateAgent core.Agent
	delegate      core.AgentSession
}

type requestState struct {
	sawText bool
}

func newSession(ctx context.Context, cfg sessionConfig, resumeID string) *Session {
	sessionCtx, cancel := context.WithCancel(ctx)
	s := &Session{
		cfg:         cfg,
		ctx:         sessionCtx,
		cancel:      cancel,
		events:      make(chan core.Event, 128),
		pending:     make(map[string]*requestState),
		deltaBuf:    make(map[string]string),
		deltaTimers: make(map[string]*time.Timer),
	}
	s.alive.Store(true)
	sessionID := sessionKeyFromEnv(cfg.sessionEnv)
	if sessionID == "" && resumeID != "" && resumeID != core.ContinueSession {
		sessionID = resumeID
	}
	if sessionID == "" {
		sessionID = "orbit:" + randomID()
	}
	s.sessionID.Store(sessionID)
	if cfg.heartbeat > 0 {
		go s.heartbeatLoop()
	}
	return s
}

func (s *Session) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("orbit: session is closed")
	}
	if s.sendToDelegate(prompt, images, files) {
		return nil
	}
	if err := s.ensureConnected(); err != nil {
		if s.cfg.fallbackAgent != "" {
			return s.startFallback(prompt, images, files, err)
		}
		return err
	}

	requestID := s.nextRequestID()
	cleanPrompt, user := s.userFromPrompt(prompt)
	content := s.buildContent(cleanPrompt, images, files)
	req := inboundRequest{
		Type:      "message.submit",
		RequestID: requestID,
		SessionID: s.CurrentSessionID(),
		User:      user,
		Content:   content,
	}
	s.pendingMu.Lock()
	s.pending[requestID] = &requestState{}
	s.pendingMu.Unlock()
	if err := s.writeFrame(req); err != nil {
		s.clearPending(requestID)
		if s.cfg.fallbackAgent != "" {
			return s.startFallback(prompt, images, files, err)
		}
		return err
	}
	return nil
}

func (s *Session) RespondPermission(requestID string, result core.PermissionResult) error {
	s.delegateMu.Lock()
	delegate := s.delegate
	s.delegateMu.Unlock()
	if delegate != nil && delegate.Alive() {
		return delegate.RespondPermission(requestID, result)
	}

	answer := strings.TrimSpace(result.Message)
	if answer == "" && result.Behavior != "" {
		answer = result.Behavior
	}
	if len(result.UpdatedInput) > 0 {
		if b, err := json.Marshal(result.UpdatedInput); err == nil {
			answer = string(b)
		}
	}
	if answer == "" {
		answer = "allow"
	}
	req := inboundRequest{
		Type:      "message.submit",
		RequestID: s.nextRequestID(),
		SessionID: s.CurrentSessionID(),
		User:      s.defaultUser(),
		Content:   &gatewayMessageContent{Kind: "text", Text: answer},
		Context:   map[string]any{"replyTo": requestID},
	}
	return s.writeFrame(req)
}

func (s *Session) Events() <-chan core.Event { return s.events }

func (s *Session) CurrentSessionID() string {
	v, _ := s.sessionID.Load().(string)
	return v
}

func (s *Session) Alive() bool { return s.alive.Load() }

func (s *Session) Close() error {
	if !s.alive.CompareAndSwap(true, false) {
		return nil
	}
	s.cancel()
	_ = s.closeConn()
	s.stopDeltaTimers()
	s.delegateMu.Lock()
	if s.delegate != nil {
		_ = s.delegate.Close()
		s.delegate = nil
	}
	if s.delegateAgent != nil {
		_ = s.delegateAgent.Stop()
		s.delegateAgent = nil
	}
	s.delegateMu.Unlock()
	s.eventMu.Lock()
	if !s.eventsClosed {
		s.eventsClosed = true
		close(s.events)
	}
	s.eventMu.Unlock()
	return nil
}

func (s *Session) ensureConnected() error {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.conn != nil {
		return nil
	}
	if strings.TrimSpace(s.cfg.socketPath) == "" {
		return fmt.Errorf("orbit: socket_path is empty")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(s.ctx, "unix", s.cfg.socketPath)
	if err != nil {
		return fmt.Errorf("orbit: connect %s: %w", s.cfg.socketPath, err)
	}
	s.conn = conn
	s.writer = bufio.NewWriter(conn)
	go s.readLoop(conn)
	slog.Info("orbit: connected external gateway", "socket_path", s.cfg.socketPath)
	return nil
}

func (s *Session) writeFrame(req inboundRequest) error {
	frame, err := encodeFrame(req)
	if err != nil {
		return fmt.Errorf("orbit: encode frame: %w", err)
	}
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.conn == nil || s.writer == nil {
		return fmt.Errorf("orbit: gateway is not connected")
	}
	if _, err := s.writer.Write(frame); err != nil {
		s.resetConnLocked()
		return fmt.Errorf("orbit: write frame: %w", err)
	}
	if err := s.writer.Flush(); err != nil {
		s.resetConnLocked()
		return fmt.Errorf("orbit: flush frame: %w", err)
	}
	return nil
}

func (s *Session) readLoop(conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		evt, err := decodeEvent(line)
		if err != nil {
			s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("orbit: decode event: %w", err)})
			continue
		}
		s.handleGatewayEvent(evt)
	}
	if s.ctx.Err() == nil {
		if err := scanner.Err(); err != nil {
			slog.Warn("orbit: gateway read failed", "error", err)
			s.failPending(fmt.Errorf("orbit: gateway read failed: %w", err))
		} else {
			s.failPending(fmt.Errorf("orbit: gateway connection closed"))
		}
	}
	s.connMu.Lock()
	if s.conn == conn {
		s.resetConnLocked()
	}
	s.connMu.Unlock()
}

func (s *Session) resetConnLocked() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.conn = nil
	s.writer = nil
}

func (s *Session) closeConn() error {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.conn == nil {
		return nil
	}
	req := inboundRequest{Type: "session.close", SessionID: s.CurrentSessionID()}
	if frame, err := encodeFrame(req); err == nil && s.writer != nil {
		_, _ = s.writer.Write(frame)
		_ = s.writer.Flush()
	}
	err := s.conn.Close()
	s.resetConnLocked()
	return err
}

func (s *Session) heartbeatLoop() {
	ticker := time.NewTicker(s.cfg.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.connMu.Lock()
			connected := s.conn != nil
			s.connMu.Unlock()
			if connected {
				if err := s.writeFrame(inboundRequest{Type: "ping"}); err != nil {
					slog.Debug("orbit: heartbeat failed", "error", err)
				}
				continue
			}
			if s.cfg.autoReconnect {
				if err := s.ensureConnected(); err != nil {
					slog.Debug("orbit: reconnect failed", "error", err)
				}
			}
		}
	}
}

func (s *Session) handleGatewayEvent(evt *outboundEvent) {
	switch evt.Type {
	case "pong":
		return
	case "request.accepted":
		if evt.RoutedTo != "" {
			s.emit(core.Event{Type: core.EventThinking, Content: "Orbit routed to " + evt.RoutedTo})
		}
	case "progress":
		s.emit(core.Event{Type: core.EventThinking, Content: formatProgress(evt)})
	case "text.delta":
		if evt.Text != "" {
			s.emitTextDelta(evt.RequestID, evt.Text)
		}
	case "artifact":
		s.flushDelta(evt.RequestID)
		msg := formatArtifact(evt)
		if msg != "" {
			s.markText(evt.RequestID)
			s.emit(core.Event{Type: core.EventText, Content: msg, SessionID: s.CurrentSessionID()})
		}
	case "card":
		s.flushDelta(evt.RequestID)
		msg := formatCard(evt.Card)
		if msg != "" {
			s.markText(evt.RequestID)
			s.emit(core.Event{Type: core.EventText, Content: msg, SessionID: s.CurrentSessionID()})
		}
	case "file":
		s.flushDelta(evt.RequestID)
		msg := formatFile(evt)
		if msg != "" {
			s.markText(evt.RequestID)
			s.emit(core.Event{Type: core.EventText, Content: msg, SessionID: s.CurrentSessionID()})
		}
	case "human_input.required":
		s.flushDelta(evt.RequestID)
		s.emitHumanInput(evt)
	case "delegate":
		s.flushDelta(evt.RequestID)
		s.clearPending(evt.RequestID)
		s.handleDelegate(evt)
	case "request.completed":
		s.handleCompleted(evt)
	case "request.failed":
		s.flushDelta(evt.RequestID)
		s.clearPending(evt.RequestID)
		msg := "Orbit request failed."
		if evt.Error != nil {
			msg = strings.TrimSpace(evt.Error.Message)
			if evt.Error.Code != "" {
				msg = evt.Error.Code + ": " + msg
			}
		}
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", msg)})
	case "request.rejected":
		s.flushDelta(evt.RequestID)
		s.clearPending(evt.RequestID)
		reason := strings.TrimSpace(evt.Reason)
		if reason == "" {
			reason = "request rejected"
		}
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("orbit: %s", reason)})
	case "notification":
		msg := formatNotification(evt)
		if msg != "" {
			s.emit(core.Event{Type: core.EventText, Content: msg, Synthetic: true, SessionID: s.CurrentSessionID()})
			s.emit(core.Event{Type: core.EventResult, Content: "", Done: true, SessionID: s.CurrentSessionID(), Synthetic: true})
		}
	default:
		slog.Debug("orbit: unhandled gateway event", "type", evt.Type)
	}
}

func (s *Session) handleCompleted(evt *outboundEvent) {
	s.flushDelta(evt.RequestID)
	state := s.clearPending(evt.RequestID)
	content := ""
	if state == nil || !state.sawText {
		content = strings.TrimSpace(evt.Text)
		if content == "" {
			content = strings.TrimSpace(evt.Detail)
		}
		if content == "" {
			content = strings.TrimSpace(evt.Reason)
		}
	}
	s.emit(core.Event{Type: core.EventResult, Content: content, Done: true, SessionID: s.CurrentSessionID()})
}

func (s *Session) handleDelegate(evt *outboundEvent) {
	target := strings.TrimSpace(evt.TargetAgent)
	if target == "" {
		target = s.cfg.fallbackAgent
	}
	workDir := strings.TrimSpace(evt.WorkingDirectory)
	if workDir == "" {
		workDir = s.cfg.workDir
	}
	opts := ensureWorkDirOption(s.cfg.delegateOptions, workDir)
	agent, err := createAgent(target, opts)
	if err != nil {
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("orbit: delegate to %s: %w", target, err)})
		return
	}
	if inj, ok := agent.(core.SessionEnvInjector); ok {
		inj.SetSessionEnv(s.cfg.sessionEnv)
	}
	if ppi, ok := agent.(core.PlatformPromptInjector); ok {
		ppi.SetPlatformPrompt(s.cfg.platformPrompt)
	}
	agentSession, err := agent.StartSession(s.ctx, "")
	if err != nil {
		_ = agent.Stop()
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("orbit: start delegate %s: %w", target, err)})
		return
	}
	s.delegateMu.Lock()
	if s.delegate != nil {
		_ = s.delegate.Close()
	}
	if s.delegateAgent != nil {
		_ = s.delegateAgent.Stop()
	}
	s.delegateAgent = agent
	s.delegate = agentSession
	s.delegateMu.Unlock()
	s.emit(core.Event{Type: core.EventThinking, Content: "Orbit delegated to " + target})
	go s.forwardDelegateEvents(agentSession)
	if err := agentSession.Send(evt.EnrichedPrompt, nil, nil); err != nil {
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("orbit: send delegate prompt: %w", err)})
	}
}

func (s *Session) startFallback(prompt string, images []core.ImageAttachment, files []core.FileAttachment, cause error) error {
	target := strings.TrimSpace(s.cfg.fallbackAgent)
	opts := ensureWorkDirOption(s.cfg.fallbackOptions, s.cfg.workDir)
	agent, err := createAgent(target, opts)
	if err != nil {
		return fmt.Errorf("orbit: gateway unavailable (%v), fallback %s unavailable: %w", cause, target, err)
	}
	if inj, ok := agent.(core.SessionEnvInjector); ok {
		inj.SetSessionEnv(s.cfg.sessionEnv)
	}
	if ppi, ok := agent.(core.PlatformPromptInjector); ok {
		ppi.SetPlatformPrompt(s.cfg.platformPrompt)
	}
	agentSession, err := agent.StartSession(s.ctx, "")
	if err != nil {
		_ = agent.Stop()
		return fmt.Errorf("orbit: gateway unavailable (%v), start fallback %s: %w", cause, target, err)
	}
	s.delegateMu.Lock()
	s.delegateAgent = agent
	s.delegate = agentSession
	s.delegateMu.Unlock()
	s.emit(core.Event{Type: core.EventThinking, Content: "Orbit gateway unavailable; falling back to " + target})
	go s.forwardDelegateEvents(agentSession)
	return agentSession.Send(prompt, images, files)
}

func (s *Session) sendToDelegate(prompt string, images []core.ImageAttachment, files []core.FileAttachment) bool {
	s.delegateMu.Lock()
	delegate := s.delegate
	s.delegateMu.Unlock()
	if delegate == nil || !delegate.Alive() {
		return false
	}
	if err := delegate.Send(prompt, images, files); err != nil {
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("orbit: delegate send: %w", err)})
	}
	return true
}

func (s *Session) forwardDelegateEvents(delegate core.AgentSession) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case evt, ok := <-delegate.Events():
			if !ok {
				return
			}
			if evt.SessionID == "" {
				evt.SessionID = s.CurrentSessionID()
			}
			s.emit(evt)
			if evt.Type == core.EventResult || evt.Type == core.EventError {
				return
			}
		}
	}
}

func (s *Session) emitHumanInput(evt *outboundEvent) {
	q := core.UserQuestion{
		Question: strings.TrimSpace(evt.Prompt),
		Header:   "Orbit needs input",
	}
	if q.Question == "" {
		q.Question = "Orbit needs your input."
	}
	for _, opt := range evt.Options {
		label := strings.TrimSpace(opt.Label)
		if label == "" {
			label = opt.ID
		}
		if label != "" {
			q.Options = append(q.Options, core.UserQuestionOption{Label: label})
		}
	}
	s.emit(core.Event{
		Type:      core.EventPermissionRequest,
		ToolName:  "AskUserQuestion",
		RequestID: evt.RequestID,
		Questions: []core.UserQuestion{q},
	})
}

func (s *Session) emit(evt core.Event) {
	s.eventMu.RLock()
	defer s.eventMu.RUnlock()
	if s.eventsClosed {
		return
	}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

func (s *Session) emitTextDelta(requestID, text string) {
	s.markText(requestID)
	if s.cfg.throttleDelta <= 0 || requestID == "" {
		s.emit(core.Event{Type: core.EventText, Content: text, SessionID: s.CurrentSessionID()})
		return
	}
	s.deltaMu.Lock()
	s.deltaBuf[requestID] += text
	if _, ok := s.deltaTimers[requestID]; !ok {
		s.deltaTimers[requestID] = time.AfterFunc(s.cfg.throttleDelta, func() {
			s.flushDelta(requestID)
		})
	}
	s.deltaMu.Unlock()
}

func (s *Session) flushDelta(requestID string) {
	if requestID == "" {
		return
	}
	s.deltaMu.Lock()
	text := s.deltaBuf[requestID]
	delete(s.deltaBuf, requestID)
	if timer := s.deltaTimers[requestID]; timer != nil {
		timer.Stop()
		delete(s.deltaTimers, requestID)
	}
	s.deltaMu.Unlock()
	if text != "" {
		s.emit(core.Event{Type: core.EventText, Content: text, SessionID: s.CurrentSessionID()})
	}
}

func (s *Session) stopDeltaTimers() {
	s.deltaMu.Lock()
	for requestID, timer := range s.deltaTimers {
		timer.Stop()
		delete(s.deltaTimers, requestID)
		delete(s.deltaBuf, requestID)
	}
	s.deltaMu.Unlock()
}

func (s *Session) failPending(err error) {
	s.pendingMu.Lock()
	hasPending := len(s.pending) > 0
	s.pending = make(map[string]*requestState)
	s.pendingMu.Unlock()
	if hasPending && s.alive.Load() {
		s.emit(core.Event{Type: core.EventError, Error: err})
	}
}

func (s *Session) markText(requestID string) {
	if requestID == "" {
		return
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if st := s.pending[requestID]; st != nil {
		st.sawText = true
	}
}

func (s *Session) clearPending(requestID string) *requestState {
	if requestID == "" {
		return nil
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	st := s.pending[requestID]
	delete(s.pending, requestID)
	return st
}

func (s *Session) nextRequestID() string {
	return fmt.Sprintf("cc-%d-%d", time.Now().UnixNano(), s.requestSeq.Add(1))
}

func (s *Session) userFromPrompt(prompt string) (string, *gatewayUser) {
	clean, user := parseSenderHeader(prompt)
	if user != nil {
		return clean, user
	}
	return prompt, s.defaultUser()
}

func (s *Session) defaultUser() *gatewayUser {
	sessionID := s.CurrentSessionID()
	parts := strings.Split(sessionID, ":")
	userID := sessionID
	if len(parts) > 1 {
		userID = parts[len(parts)-1]
	}
	platform := "cc-connect"
	if len(parts) > 0 && parts[0] != "" {
		platform = parts[0]
	}
	return &gatewayUser{Platform: platform, ID: userID}
}

func (s *Session) buildContent(prompt string, images []core.ImageAttachment, files []core.FileAttachment) *gatewayMessageContent {
	imagePaths := saveImagesToDisk(s.cfg.workDir, images)
	filePaths := core.SaveFilesToDisk(s.cfg.workDir, files)
	prompt = strings.TrimSpace(prompt)
	if len(imagePaths) == 1 && len(filePaths) == 0 {
		return &gatewayMessageContent{Kind: "image", Path: imagePaths[0], Caption: prompt}
	}
	if len(filePaths) == 1 && len(imagePaths) == 0 && prompt == "" {
		name := filepath.Base(filePaths[0])
		mime := ""
		if len(files) == 1 {
			mime = files[0].MimeType
		}
		return &gatewayMessageContent{Kind: "file", Path: filePaths[0], Name: name, Mime: mime}
	}
	allPaths := append(imagePaths, filePaths...)
	return &gatewayMessageContent{Kind: "text", Text: core.AppendFileRefs(prompt, allPaths)}
}

var senderHeaderRE = regexp.MustCompile(`^\[cc-connect\s+([^\]]+)\]\n?`)

func parseSenderHeader(prompt string) (string, *gatewayUser) {
	m := senderHeaderRE.FindStringSubmatch(prompt)
	if len(m) != 2 {
		return prompt, nil
	}
	attrs := parseHeaderAttrs(m[1])
	userID := attrs["sender_id"]
	if userID == "" {
		return prompt, nil
	}
	platform := attrs["platform"]
	if platform == "" {
		platform = "cc-connect"
	}
	return strings.TrimPrefix(prompt[len(m[0]):], "\n"), &gatewayUser{
		Platform: platform,
		ID:       userID,
		Name:     attrs["sender_name"],
	}
}

func parseHeaderAttrs(raw string) map[string]string {
	out := make(map[string]string)
	for len(raw) > 0 {
		raw = strings.TrimLeft(raw, " \t")
		if raw == "" {
			break
		}
		keyEnd := strings.IndexByte(raw, '=')
		if keyEnd <= 0 {
			break
		}
		key := raw[:keyEnd]
		raw = raw[keyEnd+1:]
		var val string
		if strings.HasPrefix(raw, `"`) {
			raw = raw[1:]
			end := strings.IndexByte(raw, '"')
			if end < 0 {
				val = raw
				raw = ""
			} else {
				val = raw[:end]
				raw = raw[end+1:]
			}
		} else {
			end := strings.IndexAny(raw, " \t")
			if end < 0 {
				val = raw
				raw = ""
			} else {
				val = raw[:end]
				raw = raw[end+1:]
			}
		}
		out[key] = val
	}
	return out
}

func sessionKeyFromEnv(env []string) string {
	for _, item := range env {
		if strings.HasPrefix(item, "CC_SESSION_KEY=") {
			return strings.TrimPrefix(item, "CC_SESSION_KEY=")
		}
	}
	return ""
}

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func saveImagesToDisk(workDir string, images []core.ImageAttachment) []string {
	if len(images) == 0 {
		return nil
	}
	attachDir := filepath.Join(workDir, ".cc-connect", "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Error("orbit: create attachments dir", "error", err)
		return nil
	}
	paths := make([]string, 0, len(images))
	for i, img := range images {
		ext := ".png"
		switch img.MimeType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		}
		name := img.FileName
		if name == "" {
			name = fmt.Sprintf("image_%d_%d%s", time.Now().UnixMilli(), i, ext)
		}
		path := filepath.Join(attachDir, filepath.Base(name))
		if err := os.WriteFile(path, img.Data, 0o644); err != nil {
			slog.Error("orbit: save image", "error", err)
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

func formatProgress(evt *outboundEvent) string {
	stage := strings.TrimSpace(evt.Stage)
	detail := strings.TrimSpace(evt.Detail)
	if stage == "" {
		stage = "working"
	}
	if detail == "" {
		return "Orbit: " + stage
	}
	return "Orbit: " + stage + "\n" + detail
}

func formatArtifact(evt *outboundEvent) string {
	var b strings.Builder
	if evt.Kind != "" {
		b.WriteString("Artifact: ")
		b.WriteString(evt.Kind)
	} else {
		b.WriteString("Artifact")
	}
	if evt.Ref != "" {
		b.WriteString(" ")
		b.WriteString(evt.Ref)
	}
	if len(evt.Preview) > 0 && string(evt.Preview) != "null" {
		b.WriteString("\n")
		b.WriteString(compactJSON(evt.Preview))
	}
	return b.String()
}

func formatCard(card *gatewayCardDefinition) string {
	if card == nil {
		return ""
	}
	parts := []string{}
	if strings.TrimSpace(card.Title) != "" {
		parts = append(parts, "**"+strings.TrimSpace(card.Title)+"**")
	}
	if strings.TrimSpace(card.Body) != "" {
		parts = append(parts, strings.TrimSpace(card.Body))
	}
	if strings.TrimSpace(card.URL) != "" {
		parts = append(parts, strings.TrimSpace(card.URL))
	}
	for _, action := range card.Actions {
		if strings.TrimSpace(action.Label) != "" {
			parts = append(parts, "["+strings.TrimSpace(action.Label)+"]")
		}
	}
	return strings.Join(parts, "\n")
}

func formatFile(evt *outboundEvent) string {
	path := strings.TrimSpace(evt.Path)
	if path == "" {
		return ""
	}
	if evt.Mime != "" {
		return fmt.Sprintf("File: %s (%s)", path, evt.Mime)
	}
	return "File: " + path
}

func formatNotification(evt *outboundEvent) string {
	if evt.Content == nil || strings.TrimSpace(evt.Content.Text) == "" {
		return ""
	}
	return strings.TrimSpace(evt.Content.Text)
}

func compactJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	switch x := v.(type) {
	case map[string]any:
		if title, _ := x["title"].(string); strings.TrimSpace(title) != "" {
			if excerpt, _ := x["excerpt"].(string); strings.TrimSpace(excerpt) != "" {
				return title + "\n" + excerpt
			}
			return title
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

var _ core.AgentSession = (*Session)(nil)
