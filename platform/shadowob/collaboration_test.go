package shadowob

import (
	"strings"
	"testing"
)

func TestMessageBuddyMentionUserIDs(t *testing.T) {
	msg := shadowMessage{
		Metadata: map[string]any{
			"mentions": []any{
				map[string]any{"kind": "buddy", "userId": "bot-1", "targetId": "bot-1"},
				map[string]any{"kind": "user", "userId": "human-1", "targetId": "human-1"},
				map[string]any{"kind": "user", "userId": "bot-2", "targetId": "bot-2", "isBot": true},
				map[string]any{"kind": "buddy", "userId": "bot-1", "targetId": "bot-1"},
			},
		},
	}
	got := messageBuddyMentionUserIDs(msg)
	if len(got) != 2 || got[0] != "bot-1" || got[1] != "bot-2" {
		t.Fatalf("messageBuddyMentionUserIDs = %#v", got)
	}
	if !messageMentionsAnyBuddy(msg) {
		t.Fatal("messageMentionsAnyBuddy = false")
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

func TestFormatBuddyThreadCoordinationPromptIncludesRuntimeRules(t *testing.T) {
	prompt := formatBuddyThreadCoordinationPrompt(&buddyThreadCoordination{
		rootMessageID:     "root-1",
		threadID:          "thread-1",
		buddyUserIDs:      []string{"bot-1", "bot-2"},
		otherBuddyUserIDs: []string{"bot-2"},
		reactionEmoji:     "👌",
	})

	for _, want := range []string{
		"Shadow multi-Buddy Thread context:",
		"already created the Thread",
		"added the 👌 coordination reaction",
		"selected this Buddy as the first speaker",
		"Do not run shell commands, Shadow CLI/API calls, browser actions",
		"cc-connect will route your response into the Thread",
		"Other mentioned Buddies will not answer the root message directly",
		"<@bot-2>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{"root-1", "thread-1"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt should not expose internal id %q:\n%s", forbidden, prompt)
		}
	}
}
