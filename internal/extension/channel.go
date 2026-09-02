package extension

import (
	"context"

	"github.com/Tencent/WeKnora/docreader/client"
	"github.com/Tencent/WeKnora/internal/utils"
	"google.golang.org/grpc"
)

// Channel is the transport-neutral carrier between the extension host and a
// running extension process. Conn() returns an opaque carrier (today a
// *grpc.ClientConn) so this interface never names a concrete transport type —
// a future WASM channel can satisfy it unchanged.
type Channel interface {
	Conn() any
	Healthy(ctx context.Context) error
	Reconnect(ctx context.Context) error
	Close() error
}

// buildDialOptions returns the gRPC dial options for an extension channel.
//
// Instead of re-implementing TLS / token construction here, we reuse
// docreader's single-sourced auth helpers (github.com/Tencent/WeKnora/docreader/client).
// Both this host and the docparser gRPC client (grpc_parser.go) now build their
// dial options from the same code, so they cannot drift on security defaults
// (TLS min version, mTLS pairing rule, bearer-token-requires-TLS guard).
func buildDialOptions() ([]grpc.DialOption, error) {
	return client.LoadAuthConfigFromEnv().BuildDialOptions(maxMessageSize())
}

// maxMessageSize returns the gRPC max recv/send size, sourced from the single
// canonical helper (utils.GetMaxFileSize, backed by MAX_FILE_SIZE_MB) rather
// than a private copy of the 50 MB default.
func maxMessageSize() int {
	return int(utils.GetMaxFileSize())
}
