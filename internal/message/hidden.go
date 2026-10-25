package message

import "context"

type hiddenUserMessageKey struct{}

// WithHiddenUserMessage marks a generated continuation for model history only.
func WithHiddenUserMessage(ctx context.Context) context.Context {
	return context.WithValue(ctx, hiddenUserMessageKey{}, true)
}

// HiddenUserMessage reports whether the caller requested a hidden continuation.
func HiddenUserMessage(ctx context.Context) bool {
	hidden, _ := ctx.Value(hiddenUserMessageKey{}).(bool)
	return hidden
}

type noticeUserMessageKey struct{}

// WithNoticeUserMessage marks a hidden continuation as a notice the chat
// shows (TextContent.Notice).
func WithNoticeUserMessage(ctx context.Context) context.Context {
	return context.WithValue(ctx, noticeUserMessageKey{}, true)
}

// NoticeUserMessage reports whether the caller marked its continuation a
// notice.
func NoticeUserMessage(ctx context.Context) bool {
	notice, _ := ctx.Value(noticeUserMessageKey{}).(bool)
	return notice
}
