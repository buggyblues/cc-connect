package orbit

import (
	"encoding/json"
	"fmt"
	"strings"
)

const protocolVersion = 1

type inboundRequest struct {
	Version   int                    `json:"version,omitempty"`
	Type      string                 `json:"type"`
	RequestID string                 `json:"requestId,omitempty"`
	SessionID string                 `json:"sessionId,omitempty"`
	User      *gatewayUser           `json:"user,omitempty"`
	Content   *gatewayMessageContent `json:"content,omitempty"`
	Context   map[string]any         `json:"context,omitempty"`
}

type gatewayUser struct {
	Platform string `json:"platform"`
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
}

type gatewayMessageContent struct {
	Kind    string `json:"kind"`
	Text    string `json:"text,omitempty"`
	Path    string `json:"path,omitempty"`
	Caption string `json:"caption,omitempty"`
	Name    string `json:"name,omitempty"`
	Mime    string `json:"mime,omitempty"`
	URL     string `json:"url,omitempty"`
}

type outboundEvent struct {
	Version          int                    `json:"version,omitempty"`
	Type             string                 `json:"type"`
	RequestID        string                 `json:"requestId,omitempty"`
	RoutedTo         string                 `json:"routedTo,omitempty"`
	Reason           string                 `json:"reason,omitempty"`
	Stage            string                 `json:"stage,omitempty"`
	Detail           string                 `json:"detail,omitempty"`
	Text             string                 `json:"text,omitempty"`
	Kind             string                 `json:"kind,omitempty"`
	Ref              string                 `json:"ref,omitempty"`
	Preview          json.RawMessage        `json:"preview,omitempty"`
	Card             *gatewayCardDefinition `json:"card,omitempty"`
	Path             string                 `json:"path,omitempty"`
	Mime             string                 `json:"mime,omitempty"`
	Prompt           string                 `json:"prompt,omitempty"`
	Options          []gatewayHumanOption   `json:"options,omitempty"`
	Error            *gatewayError          `json:"error,omitempty"`
	TargetAgent      string                 `json:"targetAgent,omitempty"`
	EnrichedPrompt   string                 `json:"enrichedPrompt,omitempty"`
	WorkingDirectory string                 `json:"workingDirectory,omitempty"`
	Target           *gatewayTargetUser     `json:"target,omitempty"`
	Content          *gatewayNotification   `json:"content,omitempty"`
}

type gatewayCardDefinition struct {
	Title   string              `json:"title"`
	Body    string              `json:"body,omitempty"`
	URL     string              `json:"url,omitempty"`
	Actions []gatewayCardAction `json:"actions,omitempty"`
}

type gatewayCardAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Style string `json:"style,omitempty"`
}

type gatewayHumanOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Style string `json:"style,omitempty"`
}

type gatewayError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type gatewayTargetUser struct {
	Platform string `json:"platform"`
	UserID   string `json:"userId"`
}

type gatewayNotification struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func encodeFrame(req inboundRequest) ([]byte, error) {
	req.Version = protocolVersion
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func decodeEvent(line []byte) (*outboundEvent, error) {
	var evt outboundEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return nil, err
	}
	if evt.Version != 0 && evt.Version != protocolVersion {
		return nil, fmt.Errorf("unsupported protocol version %d", evt.Version)
	}
	if strings.TrimSpace(evt.Type) == "" {
		return nil, fmt.Errorf("missing event type")
	}
	return &evt, nil
}

func isTerminalEvent(t string) bool {
	switch t {
	case "request.completed", "request.failed", "request.rejected", "delegate":
		return true
	default:
		return false
	}
}
