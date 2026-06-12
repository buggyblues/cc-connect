package shadowob

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

const (
	buddyThreadCoordinationReaction = "👌"
	buddyDiscussionMetadataKey      = "shadowBuddyDiscussion"
	defaultBuddyDiscussionMaxTurns  = 4
	maxBuddyDiscussionMaxTurns      = 8
)

type buddyThreadCoordination struct {
	rootMessageID     string
	threadID          string
	buddyUserIDs      []string
	otherBuddyUserIDs []string
	reactionEmoji     string
	discussion        *buddyThreadDiscussionState
}

type buddyThreadDiscussionState struct {
	rootMessageID string
	threadID      string
	buddyUserIDs  []string
	turn          int
	maxTurns      int
	speakerUserID string
}

type shadowThread struct {
	ID              string `json:"id"`
	ChannelID       string `json:"channelId"`
	ParentMessageID string `json:"parentMessageId"`
}

type shadowReactionGroup struct {
	Emoji   string   `json:"emoji"`
	Count   int      `json:"count"`
	UserIDs []string `json:"userIds"`
}

func (p *Platform) coordinateBuddyThread(ctx context.Context, sm shadowMessage, config map[string]any) (*buddyThreadCoordination, bool) {
	if sm.ThreadID != "" || sm.ID == "" || p.client == nil {
		return nil, true
	}
	buddyUserIDs := messageBuddyMentionUserIDs(sm)
	if len(buddyUserIDs) < 2 {
		return nil, true
	}
	meID := p.me.ID
	if meID == "" || !stringSliceContains(buddyUserIDs, meID) {
		return nil, true
	}

	reqCtx, cancel := requestContext(ctx)
	thread, err := p.client.ensureThread(reqCtx, sm.ID, buddyDiscussionThreadName(sm.Content))
	cancel()
	if err != nil {
		slog.Debug("shadowob: multi-Buddy ensure thread failed", "channel_id", sm.ChannelID, "message_id", sm.ID, "error", err)
		return nil, false
	}
	if thread == nil || thread.ID == "" {
		slog.Debug("shadowob: multi-Buddy ensure thread returned no id", "channel_id", sm.ChannelID, "message_id", sm.ID)
		return nil, false
	}

	reqCtx, cancel = requestContext(ctx)
	err = p.client.addReaction(reqCtx, sm.ID, buddyThreadCoordinationReaction)
	cancel()
	if err != nil {
		slog.Debug("shadowob: multi-Buddy reaction failed", "channel_id", sm.ChannelID, "message_id", sm.ID, "error", err)
		return nil, false
	}

	reqCtx, cancel = requestContext(ctx)
	reactions, err := p.client.getReactions(reqCtx, sm.ID)
	cancel()
	if err != nil {
		slog.Debug("shadowob: multi-Buddy reaction read failed", "channel_id", sm.ChannelID, "message_id", sm.ID, "error", err)
		return nil, false
	}
	firstBuddyID := firstReactionBuddyUserID(reactions, buddyThreadCoordinationReaction, buddyUserIDs)
	if firstBuddyID != meID {
		slog.Debug("shadowob: multi-Buddy reaction skipped non-first Buddy", "channel_id", sm.ChannelID, "message_id", sm.ID, "first_buddy_id", firstBuddyID, "me", meID)
		return nil, false
	}
	return &buddyThreadCoordination{
		rootMessageID:     sm.ID,
		threadID:          thread.ID,
		buddyUserIDs:      buddyUserIDs,
		otherBuddyUserIDs: otherBuddyUserIDs(buddyUserIDs, meID),
		reactionEmoji:     buddyThreadCoordinationReaction,
		discussion: &buddyThreadDiscussionState{
			rootMessageID: sm.ID,
			threadID:      thread.ID,
			buddyUserIDs:  buddyUserIDs,
			turn:          1,
			maxTurns:      buddyDiscussionMaxTurns(config),
			speakerUserID: meID,
		},
	}, true
}

func messageBuddyMentionUserIDs(sm shadowMessage) []string {
	mentions, _ := sm.Metadata["mentions"].([]any)
	seen := map[string]bool{}
	ids := []string{}
	for _, raw := range mentions {
		mention, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind := stringValue(mention["kind"])
		isBot := false
		if value, ok := mention["isBot"].(bool); ok {
			isBot = value
		}
		if kind != "buddy" && !(kind == "user" && isBot) {
			continue
		}
		id := firstNonEmpty(stringValue(mention["userId"]), stringValue(mention["targetId"]))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func messageMentionsAnyBuddy(sm shadowMessage) bool {
	return len(messageBuddyMentionUserIDs(sm)) > 0
}

func buddyDiscussionThreadName(content string) string {
	preview := strings.Join(strings.Fields(content), " ")
	if len([]rune(preview)) > 80 {
		preview = string([]rune(preview)[:80])
	}
	if preview == "" {
		return "Buddy discussion"
	}
	return preview
}

func formatBuddyThreadCoordinationPrompt(coordination *buddyThreadCoordination) string {
	if coordination == nil {
		return ""
	}
	turn := 1
	maxTurns := defaultBuddyDiscussionMaxTurns
	if coordination.discussion != nil {
		turn = coordination.discussion.turn
		maxTurns = coordination.discussion.maxTurns
	}
	lines := []string{
		"Shadow multi-Buddy Thread context:",
		"- The Shadow adapter has already created the Thread, added the " + coordination.reactionEmoji + " coordination reaction, read the reaction order, and selected this Buddy as the first speaker for the root message.",
		"- Do not run shell commands, Shadow CLI/API calls, browser actions, or any other tool to inspect the Thread or reactions.",
		"- Do not add, remove, or check coordination reactions again.",
		"- Reply normally now; cc-connect will route your response into the Thread as a reply to the root message.",
		"- The original message's raw Buddy mention tokens are routing metadata; do not copy them.",
		"- Other mentioned Buddies will not answer the root message directly; they can answer later only if explicitly mentioned in this Thread.",
		"- This is Buddy discussion turn " + intString(turn) + " of " + intString(maxTurns) + ".",
		"- If the user asked for discussion, debate, review, or comparison, invite exactly one other mentioned Buddy to take the next turn by using its canonical mention token.",
		"- Never mention yourself. Use only the available follow-up token list when handing off.",
		"- Mention at most one Buddy when handing off the next turn, and keep the answer substantive before the mention.",
		"- Close without a Buddy mention when the answer is settled or the planned turn limit has been reached.",
		"- If the user's request only needs a single answer, do not invite a follow-up.",
		"- Do not send acknowledgement-only text such as \"I agree\" or \"no extra input\".",
	}
	if len(coordination.otherBuddyUserIDs) > 0 {
		lines = append(lines, "- Other Buddy mention tokens available for a follow-up: "+strings.Join(canonicalBuddyMentionTokens(coordination.otherBuddyUserIDs), ", "))
	}
	return strings.Join(lines, "\n")
}

func formatBuddyThreadFollowupPrompt(state *buddyThreadDiscussionState, meID string) string {
	lines := []string{
		"Shadow Buddy Thread follow-up context:",
		"- Another Buddy explicitly mentioned you inside this Thread.",
		"- Reply with a substantive supplement, correction, or disagreement.",
		"- Do not send acknowledgement-only text such as \"I agree\" or \"no extra input\".",
		"- Do not copy raw mention tokens from the message body.",
	}
	if state == nil {
		lines = append(lines,
			"- This message has no active Buddy discussion metadata, so treat it as a single follow-up turn.",
			"- Do not mention another Buddy unless a human explicitly asks for another round.",
		)
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "- This is Buddy discussion turn "+intString(state.turn)+" of "+intString(state.maxTurns)+".")
	nextTokens := canonicalBuddyMentionTokens(otherBuddyUserIDs(state.buddyUserIDs, meID))
	if state.turn < state.maxTurns && len(nextTokens) > 0 {
		lines = append(lines,
			"- If another round would improve the answer, invite exactly one participant for the next turn by using its canonical mention token.",
			"- Never mention yourself.",
			"- Available next-turn mention tokens: "+strings.Join(nextTokens, ", "),
		)
	} else {
		lines = append(lines, "- This is the final planned Buddy turn; close the discussion without mentioning another Buddy unless a human explicitly asks.")
	}
	return strings.Join(lines, "\n")
}

func buddyDiscussionMaxTurns(config map[string]any) int {
	maxTurns := policyConfigInt(config, "buddyThreadMaxTurns", defaultBuddyDiscussionMaxTurns)
	if maxTurns < 2 {
		return 2
	}
	if maxTurns > maxBuddyDiscussionMaxTurns {
		return maxBuddyDiscussionMaxTurns
	}
	return maxTurns
}

func nextBuddyDiscussionState(sm shadowMessage, meID string) (*buddyThreadDiscussionState, bool) {
	state := buddyDiscussionStateFromMetadata(sm.Metadata)
	if state == nil {
		return nil, true
	}
	if state.maxTurns < 2 {
		state.maxTurns = defaultBuddyDiscussionMaxTurns
	}
	if state.maxTurns > maxBuddyDiscussionMaxTurns {
		state.maxTurns = maxBuddyDiscussionMaxTurns
	}
	if state.turn >= state.maxTurns {
		return nil, false
	}
	state.turn++
	state.threadID = firstNonEmpty(state.threadID, sm.ThreadID)
	state.speakerUserID = meID
	if len(state.buddyUserIDs) == 0 {
		state.buddyUserIDs = cleanStringList([]string{messageAuthorID(sm), meID})
	}
	return state, true
}

func buddyDiscussionStateFromMetadata(metadata map[string]any) *buddyThreadDiscussionState {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata[buddyDiscussionMetadataKey].(map[string]any)
	if !ok {
		if custom, customOK := metadata["custom"].(map[string]any); customOK {
			raw, ok = custom[buddyDiscussionMetadataKey].(map[string]any)
		}
	}
	if !ok {
		return nil
	}
	turn, _ := numberInt(raw["turn"])
	maxTurns, _ := numberInt(raw["maxTurns"])
	state := &buddyThreadDiscussionState{
		rootMessageID: stringValue(raw["rootMessageId"]),
		threadID:      stringValue(raw["threadId"]),
		buddyUserIDs:  stringListValue(raw["buddyUserIds"]),
		turn:          turn,
		maxTurns:      maxTurns,
		speakerUserID: stringValue(raw["speakerUserId"]),
	}
	if state.turn <= 0 {
		state.turn = 1
	}
	if state.maxTurns <= 0 {
		state.maxTurns = defaultBuddyDiscussionMaxTurns
	}
	state.buddyUserIDs = cleanStringList(state.buddyUserIDs)
	return state
}

func buddyDiscussionMetadata(state *buddyThreadDiscussionState, speakerUserID string) map[string]any {
	if state == nil {
		return nil
	}
	buddyUserIDs := cleanStringList(state.buddyUserIDs)
	if len(buddyUserIDs) == 0 {
		return nil
	}
	return map[string]any{
		"rootMessageId": state.rootMessageID,
		"threadId":      state.threadID,
		"buddyUserIds":  buddyUserIDs,
		"turn":          state.turn,
		"maxTurns":      state.maxTurns,
		"speakerUserId": firstNonEmpty(speakerUserID, state.speakerUserID),
	}
}

func stringListValue(value any) []string {
	switch v := value.(type) {
	case []string:
		return cleanStringList(v)
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			values = append(values, stringValue(item))
		}
		return cleanStringList(values)
	case string:
		return cleanStringList(strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n' || r == '\t'
		}))
	default:
		return nil
	}
}

func otherBuddyUserIDs(buddyUserIDs []string, meID string) []string {
	out := []string{}
	for _, userID := range buddyUserIDs {
		if userID == "" || userID == meID {
			continue
		}
		out = append(out, userID)
	}
	return out
}

func canonicalBuddyMentionTokens(userIDs []string) []string {
	tokens := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		if strings.TrimSpace(userID) == "" {
			continue
		}
		tokens = append(tokens, "<@"+userID+">")
	}
	return tokens
}

func stripBuddyMentionTokens(content string, userIDs []string) string {
	out := content
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		out = strings.ReplaceAll(out, "<@"+userID+">", "")
	}
	return strings.TrimSpace(strings.Join(strings.Fields(out), " "))
}

func sanitizeBuddyDiscussionOutboundContent(content string, state *buddyThreadDiscussionState, speakerUserID string) string {
	if state == nil || strings.TrimSpace(content) == "" {
		return content
	}
	if state.turn >= state.maxTurns {
		return strings.TrimSpace(stripBuddyMentionTokensPreserveLayout(content, state.buddyUserIDs))
	}
	speakerUserID = strings.TrimSpace(speakerUserID)
	if speakerUserID == "" {
		return content
	}
	selfToken := "<@" + speakerUserID + ">"
	if !strings.Contains(content, selfToken) {
		return content
	}
	nextTokens := canonicalBuddyMentionTokens(otherBuddyUserIDs(state.buddyUserIDs, speakerUserID))
	hasOtherToken := false
	for _, token := range nextTokens {
		if strings.Contains(content, token) {
			hasOtherToken = true
			break
		}
	}
	replacement := ""
	if !hasOtherToken && len(nextTokens) > 0 {
		replacement = nextTokens[0]
	}
	return strings.TrimSpace(strings.ReplaceAll(content, selfToken, replacement))
}

func stripBuddyMentionTokensPreserveLayout(content string, userIDs []string) string {
	out := content
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		out = strings.ReplaceAll(out, "<@"+userID+">", "")
	}
	return out
}

func intString(value int) string {
	return fmt.Sprintf("%d", value)
}

func firstReactionBuddyUserID(groups []shadowReactionGroup, emoji string, buddyUserIDs []string) string {
	for _, group := range groups {
		if group.Emoji != emoji {
			continue
		}
		for _, userID := range group.UserIDs {
			if stringSliceContains(buddyUserIDs, userID) {
				return userID
			}
		}
	}
	return ""
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func policyConfigBool(config map[string]any, key string, def bool) bool {
	if config == nil {
		return def
	}
	value, ok := config[key]
	if !ok {
		return def
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	}
	return def
}

func policyConfigInt(config map[string]any, key string, def int) int {
	if config == nil {
		return def
	}
	if n, ok := numberInt(config[key]); ok {
		return n
	}
	return def
}

func policyConfigStringSet(config map[string]any, key string) map[string]bool {
	if config == nil {
		return nil
	}
	values := optionStringList(config, key)
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func senderBuddyAllowed(config map[string]any, sm shadowMessage) bool {
	candidates := []string{messageAuthorID(sm)}
	if sm.Author != nil {
		candidates = append(candidates, sm.Author.ID, sm.Author.Username)
	}
	if blacklist := policyConfigStringSet(config, "buddyBlacklist"); len(blacklist) > 0 {
		for _, candidate := range candidates {
			if blacklist[candidate] {
				return false
			}
		}
	}
	if whitelist := policyConfigStringSet(config, "buddyWhitelist"); len(whitelist) > 0 {
		for _, candidate := range candidates {
			if whitelist[candidate] {
				return true
			}
		}
		return false
	}
	return true
}
