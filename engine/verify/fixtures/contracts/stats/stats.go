// Package stats is contract v1 for simple activity numbers.
package stats

import "example.com/project/engine/base/capability"

type CountQuery struct {
	ChatID int64 `json:"chat_id"`
}
type Count struct {
	Messages int `json:"messages"`
}

var MessageCount = capability.Define[CountQuery, Count]("stats.v1.messages.count")
