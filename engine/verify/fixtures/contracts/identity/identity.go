// Package identity is contract v1 for "who is this user / what is this chat".
// Any core (any program, a CRM, a database...) can implement it.
package identity

import "example.com/project/engine/base/capability"

var ErrNotFound = capability.NewError("not_found", "identity: not found")

type UserQuery struct {
	ID int64 `json:"id"`
}
type ChatQuery struct {
	ID int64 `json:"id"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
	Name     string `json:"name,omitempty"`
	Language string `json:"language,omitempty"`
}

type Chat struct {
	ID       int64  `json:"id"`
	Name     string `json:"name,omitempty"`
	Language string `json:"language,omitempty"`
}

var (
	GetUser = capability.Define[UserQuery, User]("identity.v1.user.get")
	GetChat = capability.Define[ChatQuery, Chat]("identity.v1.chat.get")
)
