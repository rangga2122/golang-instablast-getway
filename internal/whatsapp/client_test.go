package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.mau.fi/whatsmeow"
)

func TestIsRetryableSendError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "not connected", err: whatsmeow.ErrNotConnected, want: true},
		{name: "message timeout", err: whatsmeow.ErrMessageTimedOut, want: true},
		{name: "disconnected response", err: &whatsmeow.DisconnectedError{Action: "message send"}, want: true},
		{name: "wrapped disconnected", err: fmt.Errorf("send: %w", whatsmeow.ErrNotConnected), want: true},
		{name: "caller canceled", err: context.Canceled, want: true},
		{name: "whatsmeow canceled write", err: errors.New("failed to write msg: failed to acquire lock: context canceled"), want: true},
		{name: "caller deadline", err: context.DeadlineExceeded, want: true},
		{name: "server rejected", err: errors.New("server rejected message"), want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isRetryableSendError(test.err); got != test.want {
				t.Fatalf("isRetryableSendError(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}
