package shadowob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

type buddyCollaborationMetadata struct {
	ID                 string `json:"id"`
	RootMessageID      string `json:"rootMessageId"`
	BuddyID            string `json:"buddyId"`
	Turn               int    `json:"turn"`
	Target             string `json:"target,omitempty"`
	ThreadID           string `json:"threadId,omitempty"`
	SuggestedTextLimit int    `json:"suggestedTextLimit,omitempty"`
	ReplyDensity       string `json:"replyDensity,omitempty"`
	ReplyToID          string `json:"-"`
}

type claimBuddyReplyInput struct {
	ChannelID        string `json:"channelId"`
	RootMessageID    string `json:"rootMessageId"`
	BuddyID          string `json:"buddyId"`
	ReplyToMessageID string `json:"replyToMessageId"`
	MaxTurns         int    `json:"maxTurns,omitempty"`
	Mode             string `json:"mode,omitempty"`
	PreferredTarget  string `json:"preferredTarget,omitempty"`
}

type claimBuddyReplyResult struct {
	OK              bool   `json:"ok"`
	Reason          string `json:"reason,omitempty"`
	CollaborationID string `json:"collaborationId,omitempty"`
	Turn            int    `json:"turn,omitempty"`
	ReplyToID       string `json:"replyToId,omitempty"`
	Target          string `json:"target,omitempty"`
	ThreadID        string `json:"threadId,omitempty"`
	Metadata        struct {
		Collaboration *buddyCollaborationMetadata `json:"collaboration,omitempty"`
	} `json:"metadata,omitempty"`
}

func (p *Platform) claimBuddyCollaboration(ctx context.Context, sm shadowMessage, rt channelRuntime) (*buddyCollaborationMetadata, bool) {
	if sm.ChannelID == "" || p.agentID == "" {
		return nil, true
	}
	if sm.DMChannelID != "" {
		return nil, true
	}

	isAuthorBuddy := sm.Author != nil && sm.Author.IsBot
	mode := "initial"
	rootMessageID := sm.ID
	if isAuthorBuddy {
		if !policyConfigBool(rt.Policy.Config, "replyToBuddy", true) {
			slog.Debug("shadowob: ignoring Buddy message because replyToBuddy=false", "channel_id", sm.ChannelID, "message_id", sm.ID)
			return nil, false
		}
		if !senderBuddyAllowed(rt.Policy.Config, sm) {
			slog.Debug("shadowob: ignoring Buddy message because buddy allowlist policy denied it", "channel_id", sm.ChannelID, "message_id", sm.ID)
			return nil, false
		}
		collaboration := messageBuddyCollaboration(sm.Metadata)
		if collaboration == nil || collaboration.RootMessageID == "" {
			slog.Debug("shadowob: ignoring Buddy message without collaboration claim", "channel_id", sm.ChannelID, "message_id", sm.ID)
			return nil, false
		}
		mode = "conversation"
		rootMessageID = collaboration.RootMessageID
	}

	reqCtx, cancel := requestContext(ctx)
	claim, err := p.client.claimBuddyReply(reqCtx, claimBuddyReplyInput{
		ChannelID:        sm.ChannelID,
		RootMessageID:    rootMessageID,
		BuddyID:          p.agentID,
		ReplyToMessageID: sm.ID,
		MaxTurns:         policyConfigInt(rt.Policy.Config, "maxBuddyTurns", 4),
		Mode:             mode,
	})
	cancel()
	if err != nil {
		slog.Debug("shadowob: Buddy collaboration claim failed", "channel_id", sm.ChannelID, "message_id", sm.ID, "mode", mode, "error", err)
		return nil, false
	}
	if claim == nil || !claim.OK {
		reason := "failed"
		if claim != nil && claim.Reason != "" {
			reason = claim.Reason
		}
		slog.Debug("shadowob: Buddy collaboration claim skipped message", "channel_id", sm.ChannelID, "message_id", sm.ID, "mode", mode, "reason", reason)
		return nil, false
	}
	collaboration := claim.Metadata.Collaboration
	if collaboration == nil {
		collaboration = &buddyCollaborationMetadata{
			ID:            claim.CollaborationID,
			RootMessageID: rootMessageID,
			BuddyID:       p.agentID,
			Turn:          claim.Turn,
			Target:        claim.Target,
			ThreadID:      claim.ThreadID,
		}
	}
	if collaboration.Target == "thread" && collaboration.ThreadID == "" {
		collaboration.ThreadID = claim.ThreadID
	}
	if collaboration.ReplyToID == "" {
		collaboration.ReplyToID = firstNonEmpty(claim.ReplyToID, sm.ID)
	}
	return collaboration, true
}

func messageBuddyCollaboration(metadata map[string]any) *buddyCollaborationMetadata {
	if metadata == nil {
		return nil
	}
	if collaboration := decodeBuddyCollaboration(metadata["collaboration"]); collaboration != nil {
		return collaboration
	}
	if custom, ok := metadata["custom"].(map[string]any); ok {
		return decodeBuddyCollaboration(custom["collaboration"])
	}
	return nil
}

func decodeBuddyCollaboration(value any) *buddyCollaborationMetadata {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var collaboration buddyCollaborationMetadata
	if err := json.Unmarshal(data, &collaboration); err != nil {
		return nil
	}
	if collaboration.ID == "" || collaboration.RootMessageID == "" {
		return nil
	}
	return &collaboration
}

func formatBuddyCollaborationPrompt(collaboration *buddyCollaborationMetadata) string {
	if collaboration == nil {
		return ""
	}
	lines := []string{
		"Shadow Buddy collaboration context:",
		"- Collaboration id: " + collaboration.ID,
		"- Root message id: " + collaboration.RootMessageID,
		fmt.Sprintf("- This Buddy turn: %d", collaboration.Turn),
	}
	if collaboration.Target != "" {
		lines = append(lines, "- Platform delivery target: "+collaboration.Target)
	}
	if collaboration.ThreadID != "" {
		lines = append(lines, "- Platform thread id: "+collaboration.ThreadID)
	}
	if collaboration.ReplyDensity != "" {
		lines = append(lines, "- Suggested reply density: "+collaboration.ReplyDensity)
	}
	if collaboration.SuggestedTextLimit > 0 {
		lines = append(lines, fmt.Sprintf("- Suggested text budget: about %d characters; treat this as guidance, not a hard cutoff.", collaboration.SuggestedTextLimit))
	}
	lines = append(lines,
		"- Treat the collaboration claim as permission to speak once, not permission to run tools.",
		"- The platform may route later collaboration turns into a thread. Do not announce that routing yourself.",
		"- If you only agree, prefer a structured Shadow reaction action when the runtime exposes one; otherwise stay silent instead of posting acknowledgement text.",
		"- Keep the public channel IM-friendly: one concise message, no recap unless the user asks.",
		"- Default reply budget is soft: prefer at most 120 Chinese characters or 2 short bullets, but answer fully when the user explicitly asks for depth.",
		"- For turn 2 or later, add at most one missing point in one short sentence; if you only agree, do not send a text reply.",
		"- Match the density of the triggering message. Short chat gets a short reply or no extra reply.",
		"- Add a distinct point only. If another Buddy already covered it, acknowledge briefly and stop.",
		"- Do not create memories, skills, files, demos, task cards, or tool runs unless a human explicitly asks for current action.",
		"- Runtime logs, memory updates, skill views, tool progress, and self-improvement reviews are private implementation events. Never post them as channel messages.",
		"- If the user says to stop, stay quiet, not implement, or just discuss, comply immediately and do not continue the action chain.",
	)
	return strings.Join(lines, "\n")
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
