package providerhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

func Attempt(client *http.Client, req *http.Request, admit model.ProviderAdmission) (*http.Response, bool, error) {
	var connecting, connected atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GetConn: func(string) { connecting.Store(true) },
		GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
	}))
	req.GetBody = nil
	oneShot := *client
	oneShot.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if err := admit.Check(req.Context()); err != nil {
		return nil, false, err
	}
	response, err := oneShot.Do(req)
	if err == nil {
		return response, false, nil
	}
	var dialErr *net.OpError
	if !connected.Load() {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, model.ErrProviderUnavailable
		}
		if errors.As(err, &dialErr) && dialErr.Op == "dial" {
			return nil, true, model.ErrProviderUnavailable
		}
		if connecting.Load() {
			return nil, false, model.ErrProviderUnavailable
		}
	}
	return nil, false, model.ErrProviderOutcomeUnknown
}

func ReadResponseBody(body io.ReadCloser) []byte {
	defer body.Close()
	const limit = 64 << 10
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || len(data) > limit {
		return nil
	}
	return data
}
