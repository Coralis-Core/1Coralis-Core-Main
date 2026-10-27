// Package events defines transport-neutral events any frontend or core may publish.
package events

const TopicMessageSeen = "chat.v1.message.seen"

type MessageSeen struct {
	ChatID    int64  `json:"chat_id"`
	UserID    int64  `json:"user_id"`
	MessageID int64  `json:"message_id,omitempty"`
	Text      string `json:"text,omitempty"`
}
