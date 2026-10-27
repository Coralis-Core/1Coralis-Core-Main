// Package profile (TEST FIXTURE) holds Go types for the sample composite contract.
package profile

import "example.com/project/engine/base/capability"

type Query struct {
	ChatID int64 `json:"chat_id"`
	UserID int64 `json:"user_id"`
}

type Profile struct {
	UserID   int64  `json:"user_id"`
	Name     string `json:"name,omitempty"`
	Messages *int   `json:"messages,omitempty"`
}

var Get = capability.Define[Query, Profile]("profile.v1.get")
