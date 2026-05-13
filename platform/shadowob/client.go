package shadowob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type shadowClient struct {
	baseURL string
	token   string
	http    *http.Client
}

type shadowUser struct {
	ID          string `json:"id"`
	Email       string `json:"email,omitempty"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	IsBot       bool   `json:"isBot,omitempty"`
	AgentID     string `json:"agentId,omitempty"`
}

type shadowAuthor struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	IsBot       bool   `json:"isBot,omitempty"`
}

type shadowAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Width       *int   `json:"width,omitempty"`
	Height      *int   `json:"height,omitempty"`
}

type shadowMessage struct {
	ID          string             `json:"id"`
	Content     string             `json:"content"`
	ChannelID   string             `json:"channelId"`
	DMChannelID string             `json:"dmChannelId,omitempty"`
	AuthorID    string             `json:"authorId"`
	SenderID    string             `json:"senderId,omitempty"`
	ThreadID    string             `json:"threadId,omitempty"`
	ReplyToID   string             `json:"replyToId,omitempty"`
	CreatedAt   string             `json:"createdAt"`
	UpdatedAt   string             `json:"updatedAt"`
	Author      *shadowAuthor      `json:"author,omitempty"`
	Attachments []shadowAttachment `json:"attachments,omitempty"`
	Metadata    map[string]any     `json:"metadata,omitempty"`
}

type shadowChannel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	ServerID    string `json:"serverId"`
	Description string `json:"description,omitempty"`
}

type shadowServer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type shadowDMChannel struct {
	ID      string `json:"id"`
	UserAID string `json:"userAId,omitempty"`
	UserBID string `json:"userBId,omitempty"`
	User1ID string `json:"user1Id,omitempty"`
	User2ID string `json:"user2Id,omitempty"`
}

type shadowRemoteConfig struct {
	Servers []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Slug     string `json:"slug"`
		Channels []struct {
			ID     string              `json:"id"`
			Name   string              `json:"name"`
			Policy shadowChannelPolicy `json:"policy"`
		} `json:"channels"`
	} `json:"servers"`
}

type shadowChannelPolicy struct {
	Listen      bool           `json:"listen"`
	Reply       bool           `json:"reply"`
	MentionOnly bool           `json:"mentionOnly"`
	Config      map[string]any `json:"config,omitempty"`
}

type shadowUploadResponse struct {
	URL  string `json:"url"`
	Key  string `json:"key"`
	Size int64  `json:"size"`
}

type shadowSignedMediaURL struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}

func newShadowClient(serverURL, token string) *shadowClient {
	return &shadowClient{
		baseURL: normalizeServerURL(serverURL),
		token:   token,
		http:    core.HTTPClient,
	}
}

func normalizeServerURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "https://shadowob.com"
	}
	raw = strings.TrimRight(raw, "/")
	raw = strings.TrimSuffix(raw, "/api")
	return raw
}

func (c *shadowClient) setToken(token string) {
	c.token = strings.TrimSpace(token)
}

func (c *shadowClient) getMe(ctx context.Context) (*shadowUser, error) {
	var out shadowUser
	if err := c.requestJSON(ctx, http.MethodGet, "/api/auth/me", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *shadowClient) getAgentConfig(ctx context.Context, agentID string) (*shadowRemoteConfig, error) {
	var out shadowRemoteConfig
	if err := c.requestJSON(ctx, http.MethodGet, "/api/agents/"+url.PathEscape(agentID)+"/config", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *shadowClient) sendHeartbeat(ctx context.Context, agentID string) error {
	var out map[string]any
	return c.requestJSON(ctx, http.MethodPost, "/api/agents/"+url.PathEscape(agentID)+"/heartbeat", map[string]any{}, &out)
}

func (c *shadowClient) updateAgentSlashCommands(ctx context.Context, agentID string, commands []shadowSlashCommand) error {
	var out map[string]any
	return c.requestJSON(ctx, http.MethodPut, "/api/agents/"+url.PathEscape(agentID)+"/slash-commands", map[string]any{
		"commands": commands,
	}, &out)
}

func (c *shadowClient) getChannel(ctx context.Context, channelID string) (*shadowChannel, error) {
	var out shadowChannel
	if err := c.requestJSON(ctx, http.MethodGet, "/api/channels/"+url.PathEscape(channelID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *shadowClient) listServers(ctx context.Context) ([]shadowServer, error) {
	var out []shadowServer
	if err := c.requestJSON(ctx, http.MethodGet, "/api/servers", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *shadowClient) getServerChannels(ctx context.Context, serverID string) ([]shadowChannel, error) {
	var out []shadowChannel
	if err := c.requestJSON(ctx, http.MethodGet, "/api/servers/"+url.PathEscape(serverID)+"/channels", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *shadowClient) listDMChannels(ctx context.Context) ([]shadowDMChannel, error) {
	var out []shadowDMChannel
	if err := c.requestJSON(ctx, http.MethodGet, "/api/channels/dm", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *shadowClient) getMessage(ctx context.Context, messageID string) (*shadowMessage, error) {
	var out shadowMessage
	if err := c.requestJSON(ctx, http.MethodGet, "/api/messages/"+url.PathEscape(messageID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type sendMessageOptions struct {
	ThreadID    string         `json:"threadId,omitempty"`
	ReplyToID   string         `json:"replyToId,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Attachments []any          `json:"attachments,omitempty"`
}

func (c *shadowClient) sendMessage(ctx context.Context, channelID, content string, opts sendMessageOptions) (*shadowMessage, error) {
	body := map[string]any{"content": content}
	if opts.ThreadID != "" {
		body["threadId"] = opts.ThreadID
	}
	if opts.ReplyToID != "" {
		body["replyToId"] = opts.ReplyToID
	}
	if len(opts.Metadata) > 0 {
		body["metadata"] = opts.Metadata
	}
	if len(opts.Attachments) > 0 {
		body["attachments"] = opts.Attachments
	}
	var out shadowMessage
	if err := c.requestJSON(ctx, http.MethodPost, "/api/channels/"+url.PathEscape(channelID)+"/messages", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *shadowClient) sendDMMessage(ctx context.Context, dmChannelID, content string, opts sendMessageOptions) (*shadowMessage, error) {
	body := map[string]any{"content": content}
	if opts.ThreadID != "" {
		body["threadId"] = opts.ThreadID
	}
	if opts.ReplyToID != "" {
		body["replyToId"] = opts.ReplyToID
	}
	if len(opts.Metadata) > 0 {
		body["metadata"] = opts.Metadata
	}
	if len(opts.Attachments) > 0 {
		body["attachments"] = opts.Attachments
	}
	var out shadowMessage
	if err := c.requestJSON(ctx, http.MethodPost, "/api/channels/"+url.PathEscape(dmChannelID)+"/messages", body, &out); err != nil {
		return nil, err
	}
	if out.DMChannelID == "" {
		out.DMChannelID = dmChannelID
	}
	return &out, nil
}

func (c *shadowClient) editMessage(ctx context.Context, messageID, content string) (*shadowMessage, error) {
	var out shadowMessage
	if err := c.requestJSON(ctx, http.MethodPatch, "/api/messages/"+url.PathEscape(messageID), map[string]any{"content": content}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *shadowClient) deleteMessage(ctx context.Context, messageID string) error {
	var out map[string]any
	return c.requestJSON(ctx, http.MethodDelete, "/api/messages/"+url.PathEscape(messageID), nil, &out)
}

func (c *shadowClient) uploadMedia(ctx context.Context, data []byte, filename, contentType string, messageID, dmMessageID string) (*shadowUploadResponse, error) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	bodyReader, bodyWriter := io.Pipe()
	writer := multipart.NewWriter(bodyWriter)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/media/upload", bodyReader)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if contentType != "" {
		req.Header.Set("X-Content-Type", contentType)
	}

	writeErr := make(chan error, 1)
	go func() {
		// Stream the multipart body into the request instead of staging a second full copy.
		err := writeMultipartMedia(writer, data, filename, contentType, messageID, dmMessageID)
		if err != nil {
			_ = bodyWriter.CloseWithError(err)
		} else {
			_ = bodyWriter.Close()
		}
		writeErr <- err
	}()

	resp, err := c.http.Do(req)
	if err != nil {
		_ = bodyReader.CloseWithError(err)
		if writeBodyErr := <-writeErr; writeBodyErr != nil {
			return nil, fmt.Errorf("shadowob: write multipart media: %w", writeBodyErr)
		}
		return nil, fmt.Errorf("shadowob: upload media: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = bodyReader.CloseWithError(fmt.Errorf("shadowob: upload media failed (%d)", resp.StatusCode))
		<-writeErr
		msg := core.RedactToken(sanitizeResponseBody(string(body)), c.token)
		return nil, fmt.Errorf("shadowob: upload media failed (%d): %s", resp.StatusCode, msg)
	}
	if writeBodyErr := <-writeErr; writeBodyErr != nil {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("shadowob: write multipart media: %w", writeBodyErr)
	}
	var out shadowUploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("shadowob: decode upload response: %w", err)
	}
	return &out, nil
}

func (c *shadowClient) resolveAttachmentMediaURL(ctx context.Context, attachmentID string, _ bool) (string, error) {
	if attachmentID == "" {
		return "", fmt.Errorf("shadowob: empty attachment id")
	}
	apiPath := "/api/attachments/" + url.PathEscape(attachmentID) + "/media-url?disposition=inline"
	var out shadowSignedMediaURL
	if err := c.requestJSON(ctx, http.MethodGet, apiPath, nil, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("shadowob: empty signed media url")
	}
	return out.URL, nil
}

func writeMultipartMedia(writer *multipart.Writer, data []byte, filename, contentType, messageID, dmMessageID string) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeMultipartFilename(filename)))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create multipart file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write multipart file: %w", err)
	}
	if messageID != "" {
		if err := writer.WriteField("messageId", messageID); err != nil {
			return fmt.Errorf("write messageId: %w", err)
		}
	}
	if dmMessageID != "" {
		if err := writer.WriteField("dmMessageId", dmMessageID); err != nil {
			return fmt.Errorf("write dmMessageId: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}
	return nil
}

func (c *shadowClient) downloadFile(ctx context.Context, fileURL string, maxBytes int64) ([]byte, string, string, error) {
	fullURL := fileURL
	if strings.HasPrefix(fileURL, "/") {
		fullURL = c.baseURL + fileURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	if c.token != "" && (strings.HasPrefix(fileURL, "/") || strings.HasPrefix(fullURL, c.baseURL)) {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("shadowob: download file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		msg := core.RedactToken(sanitizeResponseBody(string(body)), c.token)
		return nil, "", "", fmt.Errorf("shadowob: download file failed (%d): %s", resp.StatusCode, msg)
	}
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		return nil, "", "", fmt.Errorf("shadowob: download file exceeds limit (%d > %d)", resp.ContentLength, maxBytes)
	}
	reader := resp.Body
	if maxBytes > 0 {
		// Read one extra byte so responses without Content-Length cannot exceed the configured cap.
		reader = io.NopCloser(io.LimitReader(resp.Body, maxBytes+1))
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", "", fmt.Errorf("shadowob: read file body: %w", err)
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, "", "", fmt.Errorf("shadowob: download file exceeds limit (%d > %d)", len(data), maxBytes)
	}
	ct := resp.Header.Get("Content-Type")
	filename := "file"
	if u, err := url.Parse(fullURL); err == nil {
		base := path.Base(u.Path)
		if base != "." && base != "/" && base != "" {
			if decoded, err := url.PathUnescape(base); err == nil {
				filename = decoded
			} else {
				filename = base
			}
		}
	}
	return data, ct, filename, nil
}

func (c *shadowClient) requestJSON(ctx context.Context, method, apiPath string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("shadowob: marshal %s %s: %w", method, apiPath, err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+apiPath, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("shadowob: %s %s: %w", method, apiPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := core.RedactToken(sanitizeResponseBody(string(data)), c.token)
		return fmt.Errorf("shadowob: %s %s failed (%d): %s", method, apiPath, resp.StatusCode, msg)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("shadowob: decode %s %s: %w", method, apiPath, err)
	}
	return nil
}

func sanitizeResponseBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "(empty response)"
	}
	if !strings.Contains(body, "<") {
		if len(body) > 500 {
			return body[:500]
		}
		return body
	}
	body = htmlTagRE.ReplaceAllString(body, " ")
	body = strings.Join(strings.Fields(body), " ")
	if body == "" {
		return "(HTML error page)"
	}
	if len(body) > 200 {
		return body[:200]
	}
	return body
}

func escapeMultipartFilename(filename string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(filename)
}

func requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, 30*time.Second)
}
