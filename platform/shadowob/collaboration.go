package shadowob

import (
	"context"
	"log/slog"
	"strings"
)

const buddyThreadCoordinationReaction = "👌"

type buddyThreadCoordination struct {
	rootMessageID     string
	threadID          string
	buddyUserIDs      []string
	otherBuddyUserIDs []string
	reactionEmoji     string
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

func (p *Platform) coordinateBuddyThread(ctx context.Context, sm shadowMessage) (*buddyThreadCoordination, bool) {
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
	lines := []string{
		"Shadow multi-Buddy Thread context:",
		"- The Shadow adapter has already created the Thread, added the " + coordination.reactionEmoji + " coordination reaction, read the reaction order, and selected this Buddy as the first speaker for the root message.",
		"- Do not run shell commands, Shadow CLI/API calls, browser actions, or any other tool to inspect the Thread or reactions.",
		"- Do not add, remove, or check coordination reactions again.",
		"- Reply normally now; cc-connect will route your response into the Thread as a reply to the root message.",
		"- Other mentioned Buddies will not answer the root message directly; they can answer later only if explicitly mentioned in this Thread.",
		"- If the user asked for discussion, debate, review, or comparison, invite exactly one other mentioned Buddy to add one concise supplement or critique by using its canonical mention token.",
		"- Ask that Buddy not to mention another Buddy unless a human explicitly requests another round.",
		"- If the user's request only needs a single answer, do not invite a follow-up.",
		"- Do not send acknowledgement-only text such as \"I agree\" or \"no extra input\".",
	}
	if len(coordination.otherBuddyUserIDs) > 0 {
		lines = append(lines, "- Other Buddy mention tokens available for a follow-up: "+strings.Join(canonicalBuddyMentionTokens(coordination.otherBuddyUserIDs), ", "))
	}
	return strings.Join(lines, "\n")
}

func formatBuddyThreadFollowupPrompt() string {
	return strings.Join([]string{
		"Shadow Buddy Thread follow-up context:",
		"- Another Buddy explicitly mentioned you inside this Thread.",
		"- Reply once with a concise supplement, correction, or disagreement.",
		"- Do not mention another Buddy unless a human explicitly asks for another round.",
	}, "\n")
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
