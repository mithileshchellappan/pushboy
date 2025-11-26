package dispatch

import (
	"context"

	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type NotificationPayload struct {
	Title string
	Body  string
}

type Dispatcher interface {
	Send(ctx context.Context, token *storage.Token, payload *NotificationPayload) error
}
