package shadowob

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

var slashCommandRE = regexp.MustCompile(`^/([a-zA-Z][a-zA-Z0-9._-]{0,63})(?:\s+([\s\S]*))?$`)

type shadowSlashCommand struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description,omitempty"`
	Aliases     []string                `json:"aliases,omitempty"`
	PackID      string                  `json:"packId,omitempty"`
	SourcePath  string                  `json:"sourcePath,omitempty"`
	Body        string                  `json:"body,omitempty"`
	Interaction *shadowInteractiveBlock `json:"interaction,omitempty"`
}

type shadowInteractiveBlock struct {
	ID                   string                   `json:"id"`
	Kind                 string                   `json:"kind"`
	Prompt               string                   `json:"prompt,omitempty"`
	Buttons              []shadowInteractiveItem  `json:"buttons,omitempty"`
	Options              []shadowInteractiveItem  `json:"options,omitempty"`
	Fields               []shadowInteractiveField `json:"fields,omitempty"`
	SubmitLabel          string                   `json:"submitLabel,omitempty"`
	ResponsePrompt       string                   `json:"responsePrompt,omitempty"`
	ApprovalCommentLabel string                   `json:"approvalCommentLabel,omitempty"`
	OneShot              *bool                    `json:"oneShot,omitempty"`
}

type shadowInteractiveItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value string `json:"value,omitempty"`
	Style string `json:"style,omitempty"`
}

type shadowInteractiveField struct {
	ID           string                  `json:"id"`
	Kind         string                  `json:"kind"`
	Label        string                  `json:"label"`
	Placeholder  string                  `json:"placeholder,omitempty"`
	DefaultValue string                  `json:"defaultValue,omitempty"`
	Required     *bool                   `json:"required,omitempty"`
	Options      []shadowInteractiveItem `json:"options,omitempty"`
	MaxLength    *int                    `json:"maxLength,omitempty"`
	Min          *float64                `json:"min,omitempty"`
	Max          *float64                `json:"max,omitempty"`
}

type slashCommandMatch struct {
	Command     shadowSlashCommand
	InvokedName string
	Args        string
}

func normalizeSlashName(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "/"))
	if slashNameRE.MatchString(value) {
		return value
	}
	return ""
}

var slashNameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,63}$`)

func commandsFromCore(commands []core.BotCommandInfo) []shadowSlashCommand {
	out := make([]shadowSlashCommand, 0, len(commands))
	seen := make(map[string]bool, len(commands))
	for _, cmd := range commands {
		name := normalizeSlashName(cmd.Command)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, shadowSlashCommand{
			Name:        name,
			Description: strings.TrimSpace(cmd.Description),
			PackID:      "cc-connect",
		})
	}
	return out
}

func loadSlashCommandsFile(path string) ([]shadowSlashCommand, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	commands := make([]shadowSlashCommand, 0, len(raw))
	seen := map[string]bool{}
	for _, item := range raw {
		name := normalizeSlashName(stringValue(item["name"]))
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		cmd := shadowSlashCommand{
			Name:        name,
			Description: truncateString(strings.TrimSpace(stringValue(item["description"])), 240),
			PackID:      truncateString(strings.TrimSpace(stringValue(item["packId"])), 80),
			SourcePath:  truncateString(strings.TrimSpace(stringValue(item["sourcePath"])), 500),
			Body:        truncateString(strings.TrimSpace(stringValue(item["body"])), 20000),
			Aliases:     normalizeAliasList(item["aliases"], name),
			Interaction: normalizeInteractiveBlock(item["interaction"]),
		}
		commands = append(commands, cmd)
	}
	return commands, nil
}

func normalizeAliasList(value any, commandName string) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []string
	seen := map[string]bool{strings.ToLower(commandName): true}
	for _, item := range values {
		name := normalizeSlashName(stringValue(item))
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	return out
}

func normalizeInteractiveBlock(value any) *shadowInteractiveBlock {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	kind := strings.ToLower(strings.TrimSpace(stringValue(raw["kind"])))
	if kind == "" {
		if _, ok := raw["fields"]; ok {
			kind = "form"
		} else if _, ok := raw["options"]; ok {
			kind = "select"
		} else if _, ok := raw["buttons"]; ok {
			kind = "buttons"
		}
	}
	switch kind {
	case "buttons", "select", "form", "approval":
	default:
		return nil
	}
	block := &shadowInteractiveBlock{
		ID:                   firstNonEmpty(stringValue(raw["id"]), stringValue(raw["blockId"])),
		Kind:                 kind,
		Prompt:               truncateString(strings.TrimSpace(firstNonEmpty(stringValue(raw["prompt"]), stringValue(raw["message"]), stringValue(raw["content"]), stringValue(raw["text"]))), 2000),
		Buttons:              normalizeInteractiveItems(raw["buttons"], 8, false),
		Options:              normalizeInteractiveItems(raw["options"], 20, true),
		Fields:               normalizeInteractiveFields(raw["fields"]),
		SubmitLabel:          truncateString(strings.TrimSpace(stringValue(raw["submitLabel"])), 40),
		ResponsePrompt:       truncateString(strings.TrimSpace(stringValue(raw["responsePrompt"])), 2000),
		ApprovalCommentLabel: truncateString(strings.TrimSpace(stringValue(raw["approvalCommentLabel"])), 120),
	}
	if block.ID == "" {
		block.ID = "cc_" + randomID()
	}
	if b, ok := raw["oneShot"].(bool); ok {
		block.OneShot = &b
	}
	return block
}

func normalizeInteractiveItems(value any, max int, requireValue bool) []shadowInteractiveItem {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	items := make([]shadowInteractiveItem, 0, len(values))
	for i, v := range values {
		raw, ok := v.(map[string]any)
		if !ok {
			continue
		}
		label := firstNonEmpty(stringValue(raw["label"]), stringValue(raw["text"]), stringValue(raw["title"]), stringValue(raw["value"]), fmt.Sprintf("Option %d", i+1))
		itemValue := firstNonEmpty(stringValue(raw["value"]), stringValue(raw["id"]), label)
		id := firstNonEmpty(stringValue(raw["id"]), stringValue(raw["actionId"]), itemValue, fmt.Sprintf("option_%d", i+1))
		if label == "" || id == "" || (requireValue && itemValue == "") {
			continue
		}
		style := strings.ToLower(strings.TrimSpace(stringValue(raw["style"])))
		if style == "danger" {
			style = "destructive"
		}
		if style != "primary" && style != "secondary" && style != "destructive" {
			style = ""
		}
		items = append(items, shadowInteractiveItem{
			ID:    truncateString(id, 80),
			Label: truncateString(label, 120),
			Value: truncateString(itemValue, 2048),
			Style: style,
		})
		if len(items) >= max {
			break
		}
	}
	return items
}

func normalizeInteractiveFields(value any) []shadowInteractiveField {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	fields := make([]shadowInteractiveField, 0, len(values))
	for i, v := range values {
		raw, ok := v.(map[string]any)
		if !ok {
			continue
		}
		kind := strings.ToLower(firstNonEmpty(stringValue(raw["kind"]), stringValue(raw["type"]), "text"))
		switch kind {
		case "text", "textarea", "number", "checkbox", "select":
		default:
			continue
		}
		id := firstNonEmpty(stringValue(raw["id"]), stringValue(raw["name"]), fmt.Sprintf("field_%d", i+1))
		label := firstNonEmpty(stringValue(raw["label"]), stringValue(raw["name"]), id)
		field := shadowInteractiveField{
			ID:           truncateString(id, 80),
			Kind:         kind,
			Label:        truncateString(label, 120),
			Placeholder:  truncateString(stringValue(raw["placeholder"]), 200),
			DefaultValue: truncateString(stringValue(raw["defaultValue"]), 2048),
			Options:      normalizeInteractiveItems(raw["options"], 20, true),
		}
		if b, ok := raw["required"].(bool); ok {
			field.Required = &b
		}
		if n, ok := numberInt(raw["maxLength"]); ok {
			field.MaxLength = &n
		}
		if n, ok := numberFloat(raw["min"]); ok {
			field.Min = &n
		}
		if n, ok := numberFloat(raw["max"]); ok {
			field.Max = &n
		}
		fields = append(fields, field)
		if len(fields) >= 12 {
			break
		}
	}
	return fields
}

func matchSlashCommand(content string, commands []shadowSlashCommand) *slashCommandMatch {
	m := slashCommandRE.FindStringSubmatch(strings.TrimSpace(content))
	if m == nil {
		return nil
	}
	invoked := m[1]
	args := ""
	if len(m) > 2 {
		args = strings.TrimSpace(m[2])
	}
	key := strings.ToLower(invoked)
	for _, cmd := range commands {
		if strings.ToLower(cmd.Name) == key {
			return &slashCommandMatch{Command: cmd, InvokedName: invoked, Args: args}
		}
		for _, alias := range cmd.Aliases {
			if strings.ToLower(alias) == key {
				return &slashCommandMatch{Command: cmd, InvokedName: invoked, Args: args}
			}
		}
	}
	return nil
}

func formatSlashCommandPrompt(original string, match *slashCommandMatch) string {
	if match == nil {
		return original
	}
	parts := []string{
		fmt.Sprintf("Shadow slash command /%s was invoked.", match.Command.Name),
	}
	if match.Command.Description != "" {
		parts = append(parts, "Description: "+match.Command.Description)
	}
	if match.Command.PackID != "" {
		parts = append(parts, "Pack: "+match.Command.PackID)
	}
	parts = append(parts, "Arguments:\n"+firstNonEmpty(match.Args, "(none)"))
	if match.Command.Body != "" {
		parts = append(parts, "Command definition:\n"+match.Command.Body)
	}
	parts = append(parts, "Original message:\n"+original)
	return strings.Join(parts, "\n\n")
}

func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

func numberInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}

func numberFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	default:
		return 0, false
	}
}
