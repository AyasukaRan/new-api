package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
)

type ConversationTraceMetadata struct {
	Key, Title, Model, ParentKey, Agent, TurnKey, ParentTurnKey string
	GreetingOnly                                                bool
}

// TraceConversationMetadata accepts only the authenticated gateway user ID and an
// explicit conversation identifier. A shared prompt, client, cache key or IP is
// never sufficient to associate turns. The caller supplies already scrubbed text.
func TraceConversationMetadata(userID int, headers http.Header, requestBody string) ConversationTraceMetadata {
	var result ConversationTraceMetadata
	var clientMetadata, conversation, claudeUser gjson.Result
	gjson.Parse(requestBody).ForEach(func(field, value gjson.Result) bool {
		switch field.Str {
		case "model":
			result.Model = traceConversationLabel(value.Str, 192)
		case "client_metadata":
			clientMetadata = value
		case "conversation", "conversation_id":
			conversation = value
		case "metadata":
			claudeUser = value.Get("user_id")
		case "messages", "input", "contents":
			if value.Type == gjson.String && field.Str == "input" {
				result.Title = traceConversationLabel(value.Str, 96)
				break
			}
			value.ForEach(func(_, message gjson.Result) bool {
				role := message.Get("role").Str
				if role != "user" && !(field.Str == "contents" && role == "") {
					return true
				}
				content := message.Get("content")
				if field.Str == "contents" {
					content = message.Get("parts")
				}
				if content.Type == gjson.String {
					result.Title = traceConversationLabel(content.Str, 96)
				} else if content.IsArray() {
					content.ForEach(func(_, part gjson.Result) bool {
						kind := part.Get("type").Str
						if kind == "" || kind == "text" || kind == "input_text" {
							result.Title = traceConversationLabel(part.Get("text").Str, 96)
						}
						return result.Title == ""
					})
				}
				return result.Title == ""
			})
		}
		return true
	})
	if isGreetingText(result.Title) {
		result.GreetingOnly = isGreetingOnlyRequest(requestBody)
	}
	if userID <= 0 {
		return result
	}
	if conversation.IsObject() {
		conversation = conversation.Get("id")
	}
	var claudeSession string
	// Claude Code versions send either serialized JSON or a compound user ID.
	if len(claudeUser.Str) <= 1024 {
		if strings.HasPrefix(claudeUser.Str, "{") {
			claudeSession = gjson.Get(claudeUser.Str, "session_id").Str
		} else if strings.HasPrefix(claudeUser.Str, "user_") {
			_, claudeSession, _ = strings.Cut(claudeUser.Str, "_session_")
		}
	}
	turnMeta := gjson.Parse(headers.Get("X-Codex-Turn-Metadata"))
	if !turnMeta.IsObject() {
		value := clientMetadata.Get("x-codex-turn-metadata")
		if value.IsObject() {
			turnMeta = value
		} else {
			turnMeta = gjson.Parse(value.Str)
		}
	}
	result.Agent = traceConversationLabel(turnMeta.Get("agent_name").Str, 96)
	for _, id := range []string{
		headers.Get("Thread-Id"), headers.Get("Thread_id"), clientMetadata.Get("thread_id").Str, turnMeta.Get("thread_id").Str,
		headers.Get("X-DeepSeek-Harness-Session-Id"), headers.Get("X-Claude-Code-Session-Id"),
		headers.Get("Session-Id"), headers.Get("Session_id"), clientMetadata.Get("session_id").Str,
		conversation.Str, claudeSession,
	} {
		if result.Key = traceScopedID("c_", strconv.Itoa(userID), id); result.Key != "" {
			break
		}
	}
	if result.Key != "" {
		for _, parent := range []string{
			headers.Get("X-Codex-Parent-Thread-Id"), clientMetadata.Get("parent_thread_id").Str, turnMeta.Get("parent_thread_id").Str,
			clientMetadata.Get("parent_session_id").Str, turnMeta.Get("parent_session_id").Str,
		} {
			if candidate := traceScopedID("c_", strconv.Itoa(userID), parent); candidate != "" && candidate != result.Key {
				result.ParentKey = candidate
				break
			}
		}
	}
	if result.Key != "" {
		for _, id := range []string{turnMeta.Get("turn_id").Str, clientMetadata.Get("turn_id").Str} {
			if result.TurnKey = traceScopedID("t_", result.Key, id); result.TurnKey != "" {
				break
			}
		}
	}
	if result.ParentKey != "" {
		for _, id := range []string{turnMeta.Get("parent_turn_id").Str, clientMetadata.Get("parent_turn_id").Str} {
			if result.ParentTurnKey = traceScopedID("t_", result.ParentKey, id); result.ParentTurnKey != "" {
				break
			}
		}
	}
	return result
}

// Classify whole utterances only. A salutation followed by a question is substantive.
func isGreetingText(text string) bool {
	if len(text) > 128 {
		return false
	}
	text = strings.TrimSpace(text)
	text = strings.TrimRight(text, "!?.,~！？，。～…")
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "你好", "您好", "你好啊", "你好呀", "您好呀", "哈喽", "哈囉", "嗨", "hello", "hi", "hey", "hello there", "hi there", "早上好", "下午好", "晚上好", "good morning", "good afternoon", "good evening":
		return true
	}
	return false
}

// A preview title alone cannot distinguish a greeting from a caption on an image
// or the opening line of a replayed history. Only mark a complete text-only input.
func isGreetingOnlyRequest(body string) bool {
	if !gjson.Valid(body) {
		return false
	}
	request := gjson.Parse(body)
	if request.Get("previous_response_id").Str != "" {
		return false
	}
	for _, key := range []string{"messages", "input", "contents"} {
		messages := request.Get(key)
		if !messages.Exists() {
			continue
		}
		if messages.Type == gjson.String {
			return key == "input" && isGreetingText(messages.Str)
		}
		if !messages.IsArray() {
			return false
		}
		valid, users := true, 0
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").Str
			if role == "system" || role == "developer" {
				return true
			}
			users++
			kind := message.Get("type").Str
			if users > 1 || (role != "user" && !(role == "" && key == "contents")) ||
				(kind != "" && kind != "message") || message.Get("attachments.0").Exists() || message.Get("tool_calls.0").Exists() || message.Get("audio").Exists() {
				valid = false
				return false
			}
			content := message.Get("content")
			if key == "contents" {
				content = message.Get("parts")
			}
			if content.Type == gjson.String {
				valid = isGreetingText(content.Str)
			} else {
				part := content.Get("0")
				partType := part.Get("type").Str
				valid = content.IsArray() && !content.Get("1").Exists() &&
					(partType == "text" || partType == "input_text" || key == "contents" && partType == "") &&
					isGreetingText(part.Get("text").Str) && !part.Get("inlineData").Exists() && !part.Get("fileData").Exists() &&
					!part.Get("inline_data").Exists() && !part.Get("file_data").Exists()
			}
			return valid
		})
		return valid && users == 1
	}
	return false
}

func traceScopedID(prefix, owner, id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 256 || strings.Contains(id, redactedPlaceholder) || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return ""
	}
	digest := sha256.Sum256([]byte(owner + "\x00" + id))
	return prefix + hex.EncodeToString(digest[:24])
}

// Take a bounded, single-line preview without splitting a UTF-8 character or
// allocating in proportion to an arbitrarily long first message.
func traceConversationLabel(text string, limit int) string {
	var label strings.Builder
	count, space := 0, false
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = count > 0
			continue
		}
		if space && count+1 < limit {
			label.WriteByte(' ')
			count++
		}
		space = false
		label.WriteRune(r)
		count++
		if count == limit {
			break
		}
	}
	return label.String()
}
