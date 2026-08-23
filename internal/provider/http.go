package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
)

func HTTPClientWithoutRedirects(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	cloned.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &cloned
}

func HTTPClientForStreaming(client *http.Client) *http.Client {
	cloned := HTTPClientWithoutRedirects(client)
	cloned.Timeout = 0
	return cloned
}

func ReadResponse(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, errors.New("read upstream response")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("upstream response too large")
	}
	return data, nil
}

func TransportError(providerName string, err error) error {
	kind := ErrorUnavailable
	var netErr net.Error
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
		kind = ErrorTimeout
	}
	return NewError(kind, providerName, errors.New("upstream request failed"))
}

func HTTPStatusError(providerName string, status int) error {
	kind := ErrorInvalidRequest
	switch {
	case status == http.StatusRequestTimeout:
		kind = ErrorTimeout
	case status == http.StatusTooManyRequests:
		kind = ErrorRateLimited
	case status >= http.StatusInternalServerError:
		kind = ErrorUnavailable
	}
	return NewError(kind, providerName, fmt.Errorf("upstream status %d", status))
}
