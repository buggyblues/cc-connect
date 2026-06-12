package shadowob

import (
	"context"
	"log/slog"
	"strings"
)

const buddyThreadCoordinationReaction = "👌"

type buddyThreadCoordination struct {
	rootMessageID string
	threadID      string
	buddyUserIDs  []string
	reactionEmoji string
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
		rootMessageID: sm.ID,
		threadID:      thread.ID,
		buddyUserIDs:  buddyUserIDs,
		reactionEmoji: buddyThreadCoordinationReaction,
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
		"- Root message id: " + coordination.rootMessageID,
		"- Thread id: " + coordination.threadID,
		"- Coordination reaction: " + coordination.reactionEmoji,
		"- You are the first mentioned Buddy that sent the coordination reaction, so give one concise first reply in this Thread.",
		"- Other mentioned Buddies will remain silent after their reaction.",
		"- Do not send acknowledgement-only text such as \"I agree\" or \"no extra input\".",
	}
	return strings.Join(lines, "\n")
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
