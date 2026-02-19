package handlers

import (
	"ai-saas-dashboard/models"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ChatHandler struct {
	db            *sql.DB
	claudeAPIKey  string
	claudeAPIURL  string
}

func NewChatHandler(db *sql.DB, claudeAPIKey string) *ChatHandler {
	return &ChatHandler{
		db:           db,
		claudeAPIKey: claudeAPIKey,
		claudeAPIURL: "https://api.anthropic.com/v1/messages",
	}
}

type ChatRequest struct {
	Message        string `json:"message"`
	ConversationID string `json:"conversationId,omitempty"`
}

type ClaudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ClaudeRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	Messages    []ClaudeMessage `json:"messages"`
	Stream      bool            `json:"stream"`
	Temperature float64         `json:"temperature,omitempty"`
}

type ClaudeResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Model        string `json:"model"`
	StopReason   string `json:"stop_reason"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type StreamEvent struct {
	Type         string `json:"type"`
	Text         string `json:"text,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
}

func (h *ChatHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	if userID == "" {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.Message == "" {
		http.Error(w, `{"error":"Message is required"}`, http.StatusBadRequest)
		return
	}

	// Get or create conversation
	conversationID := req.ConversationID
	if conversationID == "" {
		var err error
		conversationID, err = h.createConversation(userID, req.Message)
		if err != nil {
			http.Error(w, `{"error":"Error creating conversation"}`, http.StatusInternalServerError)
			return
		}
	}

	// Save user message
	_, err := h.db.Exec(
		`INSERT INTO messages (conversation_id, role, content) VALUES ($1, $2, $3)`,
		conversationID, "user", req.Message,
	)
	if err != nil {
		http.Error(w, `{"error":"Error saving message"}`, http.StatusInternalServerError)
		return
	}

	// Get conversation history
	messages, err := h.getConversationMessages(conversationID)
	if err != nil {
		http.Error(w, `{"error":"Error fetching conversation history"}`, http.StatusInternalServerError)
		return
	}

	// Prepare Claude API request
	claudeMessages := make([]ClaudeMessage, len(messages))
	for i, msg := range messages {
		claudeMessages[i] = ClaudeMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	claudeReq := ClaudeRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 4096,
		Messages:  claudeMessages,
		Stream:    true,
		Temperature: 0.7,
	}

	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send initial event with conversation ID
	fmt.Fprintf(w, "data: %s\n\n", formatStreamEvent("start", "", conversationID))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Call Claude API with streaming
	assistantResponse, err := h.streamClaudeResponse(w, claudeReq)
	if err != nil {
		fmt.Fprintf(w, "data: %s\n\n", formatStreamEvent("error", err.Error(), conversationID))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	// Save assistant response
	_, err = h.db.Exec(
		`INSERT INTO messages (conversation_id, role, content) VALUES ($1, $2, $3)`,
		conversationID, "assistant", assistantResponse,
	)
	if err != nil {
		fmt.Fprintf(w, "data: %s\n\n", formatStreamEvent("error", "Error saving response", conversationID))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	// Update conversation timestamp
	_, _ = h.db.Exec(`UPDATE conversations SET updated_at = CURRENT_TIMESTAMP WHERE id = $1`, conversationID)

	// Send end event
	fmt.Fprintf(w, "data: %s\n\n", formatStreamEvent("end", "", conversationID))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (h *ChatHandler) streamClaudeResponse(w http.ResponseWriter, claudeReq ClaudeRequest) (string, error) {
	reqBody, err := json.Marshal(claudeReq)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", h.claudeAPIURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", h.claudeAPIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Claude API error: %s", string(body))
	}

	var fullResponse strings.Builder
	reader := resp.Body
	buffer := make([]byte, 4096)

	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			chunk := string(buffer[:n])
			lines := strings.Split(chunk, "\n")

			for _, line := range lines {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "data: ") {
					continue
				}

				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					break
				}

				var streamResp map[string]interface{}
				if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
					continue
				}

				if streamResp["type"] == "content_block_delta" {
					if delta, ok := streamResp["delta"].(map[string]interface{}); ok {
						if text, ok := delta["text"].(string); ok {
							fullResponse.WriteString(text)
							// Send chunk to client
							fmt.Fprintf(w, "data: %s\n\n", formatStreamEvent("content", text, ""))
							if f, ok := w.(http.Flusher); ok {
								f.Flush()
							}
						}
					}
				}
			}
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			return fullResponse.String(), err
		}
	}

	return fullResponse.String(), nil
}

func (h *ChatHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	if userID == "" {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	conversationID := r.URL.Query().Get("conversationId")
	if conversationID == "" {
		http.Error(w, `{"error":"conversationId is required"}`, http.StatusBadRequest)
		return
	}

	// Verify conversation belongs to user
	var exists bool
	err := h.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM conversations WHERE id = $1 AND user_id = $2)`,
		conversationID, userID,
	).Scan(&exists)

	if err != nil || !exists {
		http.Error(w, `{"error":"Conversation not found"}`, http.StatusNotFound)
		return
	}

	messages, err := h.getConversationMessages(conversationID)
	if err != nil {
		http.Error(w, `{"error":"Error fetching messages"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"messages": messages,
	})
}

func (h *ChatHandler) GetConversations(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	if userID == "" {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	rows, err := h.db.Query(
		`SELECT id, title, created_at, updated_at FROM conversations 
		 WHERE user_id = $1 ORDER BY updated_at DESC LIMIT 50`,
		userID,
	)
	if err != nil {
		http.Error(w, `{"error":"Error fetching conversations"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var conversations []models.Conversation
	for rows.Next() {
		var conv models.Conversation
		conv.UserID = userID
		err := rows.Scan(&conv.ID, &conv.Title, &conv.CreatedAt, &conv.UpdatedAt)
		if err != nil {
			continue
		}
		conversations = append(conversations, conv)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"conversations": conversations,
	})
}

func (h *ChatHandler) createConversation(userID, firstMessage string) (string, error) {
	title := firstMessage
	if len(title) > 50 {
		title = title[:47] + "..."
	}

	var conversationID string
	err := h.db.QueryRow(
		`INSERT INTO conversations (user_id, title) VALUES ($1, $2) RETURNING id`,
		userID, title,
	).Scan(&conversationID)

	return conversationID, err
}

func (h *ChatHandler) getConversationMessages(conversationID string) ([]models.Message, error) {
	rows, err := h.db.Query(
		`SELECT id, role, content, created_at FROM messages 
		 WHERE conversation_id = $1 ORDER BY created_at ASC`,
		conversationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []models.Message
	for rows.Next() {
		var msg models.Message
		msg.ConversationID = conversationID
		err := rows.Scan(&msg.ID, &msg.Role, &msg.Content, &msg.CreatedAt)
		if err != nil {
			continue
		}
		messages = append(messages, msg)
	}

	return messages, nil
}

func formatStreamEvent(eventType, text, conversationID string) string {
	event := StreamEvent{
		Type:           eventType,
		Text:           text,
		ConversationID: conversationID,
	}
	data, _ := json.Marshal(event)
	return string(data)
}
