package shadowob

import (
	"strings"
	"testing"
)

func TestMessageBuddyCollaborationReadsDirectAndCustomMetadata(t *testing.T) {
	direct := messageBuddyCollaboration(map[string]any{
		"collaboration": map[string]any{
			"id":            "collab-1",
			"rootMessageId": "root-1",
			"buddyId":       "buddy-1",
			"turn":          1,
		},
	})
	if direct == nil || direct.ID != "collab-1" || direct.RootMessageID != "root-1" {
		t.Fatalf("direct collaboration = %#v", direct)
	}

	custom := messageBuddyCollaboration(map[string]any{
		"custom": map[string]any{
			"collaboration": map[string]any{
				"id":            "collab-2",
				"rootMessageId": "root-2",
				"buddyId":       "buddy-2",
				"turn":          2,
			},
		},
	})
	if custom == nil || custom.ID != "collab-2" || custom.RootMessageID != "root-2" {
		t.Fatalf("custom collaboration = %#v", custom)
	}

	if invalid := messageBuddyCollaboration(map[string]any{"collaboration": map[string]any{"id": "x"}}); invalid != nil {
		t.Fatalf("invalid collaboration = %#v, want nil", invalid)
	}
}

func TestSenderBuddyAllowedAppliesBlacklistBeforeWhitelist(t *testing.T) {
	msg := shadowMessage{
		AuthorID: "buddy-user-1",
		Author:   &shadowAuthor{ID: "buddy-user-1", Username: "one"},
	}
	if senderBuddyAllowed(map[string]any{"buddyBlacklist": []any{"one"}}, msg) {
		t.Fatal("blacklisted Buddy should be denied")
	}
	if !senderBuddyAllowed(map[string]any{"buddyWhitelist": []any{"one"}}, msg) {
		t.Fatal("whitelisted Buddy should be allowed")
	}
	if senderBuddyAllowed(map[string]any{"buddyWhitelist": []any{"two"}}, msg) {
		t.Fatal("Buddy outside whitelist should be denied")
	}
}

func TestFormatBuddyCollaborationPromptIncludesRuntimeRules(t *testing.T) {
	prompt := formatBuddyCollaborationPrompt(&buddyCollaborationMetadata{
		ID:                 "collab-1",
		RootMessageID:      "root-1",
		BuddyID:            "buddy-1",
		Turn:               2,
		Target:             "thread",
		ThreadID:           "thread-1",
		SuggestedTextLimit: 120,
		ReplyDensity:       "short",
	})

	for _, want := range []string{
		"Shadow Buddy collaboration context:",
		"Collaboration id: collab-1",
		"This Buddy turn: 2",
		"Platform thread id: thread-1",
		"not permission to run tools",
		"Do not create memories",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
