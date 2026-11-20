//go:build !windows

package rpcpipe

import (
	"context"

	seamrpc "github.com/adag/ada-go/v2/plugins/seam/rpc"
)

type noTransport struct{}

func newTransport() seamrpc.Transport { return noTransport{} }

func (noTransport) Dial(context.Context, seamrpc.Address) (seamrpc.Stream, error) {
	return nil, seamrpc.Unimplemented("rpc-pipe 只在 Windows 可用")
}

func (noTransport) Listen(context.Context, seamrpc.Address, seamrpc.ListenOptions) (seamrpc.Listener, error) {
	return nil, seamrpc.Unimplemented("rpc-pipe 只在 Windows 可用")
}
