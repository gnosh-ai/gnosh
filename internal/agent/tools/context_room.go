package tools

import (
	"context"
	"sync"
)

// ContextRoom is how many more tokens reads may bring into a conversation
// in one step of it, for view's allow_large: as Claude Code allows a Read
// with allow_large, 70% of what is left before the point at which the
// conversation is compacted. The reads of a step share it.
type ContextRoom struct {
	mu   sync.Mutex
	left int
}

// NewContextRoom is a room of tokens; one below zero is none.
func NewContextRoom(tokens int) *ContextRoom {
	return &ContextRoom{left: max(tokens, 0)}
}

// Left is the room no read has claimed yet.
func (r *ContextRoom) Left() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.left
}

// Claim takes n tokens of the room, for a read that brought them.
func (r *ContextRoom) Claim(n int) {
	r.mu.Lock()
	r.left = max(r.left-n, 0)
	r.mu.Unlock()
}

type contextRoomKey struct{}

// WithContextRoom is ctx carrying room, for the tool calls of a step.
func WithContextRoom(ctx context.Context, room *ContextRoom) context.Context {
	return context.WithValue(ctx, contextRoomKey{}, room)
}

// ContextRoomFrom is the room ctx carries, or nil where there is none.
func ContextRoomFrom(ctx context.Context) *ContextRoom {
	room, _ := ctx.Value(contextRoomKey{}).(*ContextRoom)
	return room
}
