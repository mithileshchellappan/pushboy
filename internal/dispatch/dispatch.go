package dispatch

import (
	"context"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type Dispatcher interface {
	Send(ctx context.Context, token *storage.Token, payload *model.NotificationPayload) error
}
