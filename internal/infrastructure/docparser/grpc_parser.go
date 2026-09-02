package docparser

import (
	"context"
	"fmt"
	"io"

	"github.com/Tencent/WeKnora/docreader/proto"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExtensionChannel is the slice of extension.Channel that this reader needs: a
// carrier it can borrow a live connection from, plus the ability to ask that
// carrier to dial again.
//
// It deliberately omits Close(): the extension host owns the connection's
// lifetime, so "the reader closes a connection it only borrowed" is a compile
// error here rather than a convention someone has to remember. extension.Channel
// satisfies this interface structurally, so this package needs no import of
// internal/extension and the dependency between the two stays one-way.
type ExtensionChannel interface {
	// Conn returns the live carrier, today a *grpc.ClientConn, or nil when the
	// channel currently has none.
	Conn() any
	// Reconnect asks the channel to drop its carrier and dial again, using the
	// endpoint the channel itself holds.
	Reconnect(ctx context.Context) error
}

// endpointSetter is the optional capability a channel may expose to be pointed
// at a different address. It is asserted, not required, because pointing an
// extension somewhere else is the host's decision, not the reader's.
type endpointSetter interface {
	SetEndpoint(addr string)
}

// GRPCDocumentReader implements DocumentReader over gRPC.
//
// It borrows its connection from an extension channel instead of dialing one.
// The channel — and behind it the extension host — decides where to connect,
// when to dial again and who closes; this type only decides what to say over
// the wire.
//
// It therefore keeps neither a *grpc.ClientConn nor a proto.DocReaderClient of
// its own. The stub is a free derivation of the connection
// (proto.NewDocReaderClient is one struct allocation and no I/O), so deriving it
// per call is cheaper than caching it and having to invalidate that cache every
// time the owner dials again. Keeping no derived value is what makes
// "reconnected, but still talking on the old connection" impossible rather than
// merely unlikely.
//
// The single field is set at construction and never mutated, so the reader needs
// no lock of its own; the channel guards its own connection swap.
type GRPCDocumentReader struct {
	ch ExtensionChannel
}

var _ interfaces.DocumentReader = (*GRPCDocumentReader)(nil)

// NewGRPCDocumentReaderFromChannel returns a reader that borrows its connection
// from ch. Ownership of ch stays with the extension host.
func NewGRPCDocumentReaderFromChannel(ch ExtensionChannel) *GRPCDocumentReader {
	return &GRPCDocumentReader{ch: ch}
}

// NewDisconnectedGRPCDocumentReader returns a reader with no channel, for when
// docreader is not configured. Every RPC then fails with errNotConnected and
// IsConnected reports false — which is what the parser-engine endpoints and the
// scanned-PDF fallback path already expect of an unconfigured docreader.
func NewDisconnectedGRPCDocumentReader() *GRPCDocumentReader {
	return &GRPCDocumentReader{}
}

var errNotConnected = fmt.Errorf("docreader service not connected")

// conn returns the connection currently held by the channel, or nil.
//
// The assertion is to the concrete *grpc.ClientConn and the nil check is
// explicit: a nil *grpc.ClientConn carried inside an interface value is not a
// nil interface, so asserting to grpc.ClientConnInterface instead would report
// success and defer the nil to a panic on the first RPC.
func (p *GRPCDocumentReader) conn() *grpc.ClientConn {
	if p.ch == nil {
		return nil
	}
	conn, _ := p.ch.Conn().(*grpc.ClientConn)
	return conn
}

// client derives the generated stub from the borrowed connection.
func (p *GRPCDocumentReader) client() (proto.DocReaderClient, error) {
	conn := p.conn()
	if conn == nil {
		return nil, errNotConnected
	}
	return proto.NewDocReaderClient(conn), nil
}

// Reconnect asks the owner of the connection to dial again.
//
// addr is applied only if the channel accepts an endpoint change. The reader
// never dials by itself: a connection it created would be invisible to the
// extension host, which would go on handing out and health-checking the old one
// while this reader used another, and the old one would never be closed. An
// address that cannot be applied is reported as an error rather than silently
// ignored, so "reconnect succeeded" can never mean "reconnected to the address
// you did not ask for".
func (p *GRPCDocumentReader) Reconnect(addr string) error {
	if p.ch == nil {
		return fmt.Errorf(
			"%w: no extension channel to reconnect; configure the docreader endpoint on the extension host",
			errNotConnected,
		)
	}
	if addr != "" {
		setter, ok := p.ch.(endpointSetter)
		if !ok {
			return fmt.Errorf(
				"cannot point docreader at %q: this extension channel does not accept an endpoint change; "+
					"the address has to be changed on the extension host that owns the connection",
				addr,
			)
		}
		setter.SetEndpoint(addr)
	}
	return p.ch.Reconnect(context.Background())
}

// IsConnected reports whether a connection is currently available. It stays a
// cheap field read and sends no packet, because it is called on the scanned-PDF
// fallback path for every document as well as by the system endpoints.
func (p *GRPCDocumentReader) IsConnected() bool {
	return p.conn() != nil
}

func (p *GRPCDocumentReader) Read(ctx context.Context, req *types.ReadRequest) (*types.ReadResult, error) {
	client, err := p.client()
	if err != nil {
		return nil, err
	}

	protoReq := &proto.ReadRequest{
		FileContent: req.FileContent,
		FileName:    req.FileName,
		FileType:    req.FileType,
		Url:         req.URL,
		Title:       req.Title,
		RequestId:   req.RequestID,
		Config: &proto.ReadConfig{
			ParserEngine:          req.ParserEngine,
			ParserEngineOverrides: req.ParserEngineOverrides,
		},
	}

	// Use the streaming RPC so documents with many page images (large scanned
	// PDFs) are not capped by the unary message-size limit. The meta frame
	// arrives first, followed by one frame per image.
	result, err := p.readStream(ctx, client, protoReq)
	if err != nil {
		// An older docreader build may not implement ReadStream. Fall back to
		// the unary Read RPC so a version-skewed deployment still parses
		// documents (small/medium docs only — the unary path remains capped by
		// the gRPC message-size limit, which is exactly what streaming avoids).
		if status.Code(err) == codes.Unimplemented {
			logger.Warnf(ctx, "docreader ReadStream unimplemented, falling back to unary Read: %v", err)
			return p.readUnary(ctx, client, protoReq)
		}
		return nil, err
	}
	return result, nil
}

// readStream consumes the server-streaming ReadStream RPC: one meta frame
// followed by one frame per image. Errors are returned verbatim so the caller
// can inspect the gRPC status code (e.g. Unimplemented) for fallback.
func (p *GRPCDocumentReader) readStream(
	ctx context.Context, client proto.DocReaderClient, protoReq *proto.ReadRequest,
) (*types.ReadResult, error) {
	stream, err := client.ReadStream(ctx, protoReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC ReadStream failed: %w", err)
	}

	result := &types.ReadResult{}
	gotMeta := false
	for {
		frame, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return nil, fmt.Errorf("gRPC ReadStream recv failed: %w", recvErr)
		}

		if meta := frame.GetMeta(); meta != nil {
			gotMeta = true
			result.MarkdownContent = meta.GetMarkdownContent()
			result.ImageDirPath = meta.GetImageDirPath()
			result.Metadata = meta.GetMetadata()
			result.Error = meta.GetError()
			if n := meta.GetImageCount(); n > 0 {
				result.ImageRefs = make([]types.ImageRef, 0, n)
			}
			continue
		}

		if img := frame.GetImage(); img != nil {
			result.ImageRefs = append(result.ImageRefs, types.ImageRef{
				Filename:    img.GetFilename(),
				OriginalRef: img.GetOriginalRef(),
				MimeType:    img.GetMimeType(),
				StorageKey:  img.GetStorageKey(),
				ImageData:   img.GetImageData(),
			})
		}
	}

	if !gotMeta {
		return nil, fmt.Errorf("gRPC ReadStream returned no metadata frame")
	}
	return result, nil
}

// readUnary calls the legacy unary Read RPC. Used only as a compatibility
// fallback when the connected docreader does not implement ReadStream.
func (p *GRPCDocumentReader) readUnary(
	ctx context.Context, client proto.DocReaderClient, protoReq *proto.ReadRequest,
) (*types.ReadResult, error) {
	resp, err := client.Read(ctx, protoReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC Read failed: %w", err)
	}

	result := &types.ReadResult{
		MarkdownContent: resp.GetMarkdownContent(),
		ImageDirPath:    resp.GetImageDirPath(),
		Metadata:        resp.GetMetadata(),
		Error:           resp.GetError(),
	}
	if refs := resp.GetImageRefs(); len(refs) > 0 {
		result.ImageRefs = make([]types.ImageRef, 0, len(refs))
		for _, img := range refs {
			result.ImageRefs = append(result.ImageRefs, types.ImageRef{
				Filename:    img.GetFilename(),
				OriginalRef: img.GetOriginalRef(),
				MimeType:    img.GetMimeType(),
				StorageKey:  img.GetStorageKey(),
				ImageData:   img.GetImageData(),
			})
		}
	}
	return result, nil
}

func (p *GRPCDocumentReader) ListEngines(
	ctx context.Context, overrides map[string]string,
) ([]types.ParserEngineInfo, error) {
	client, err := p.client()
	if err != nil {
		return nil, err
	}

	resp, err := client.ListEngines(ctx, &proto.ListEnginesRequest{ConfigOverrides: overrides})
	if err != nil {
		return nil, fmt.Errorf("gRPC ListEngines failed: %w", err)
	}

	result := make([]types.ParserEngineInfo, 0, len(resp.GetEngines()))
	for _, e := range resp.GetEngines() {
		result = append(result, types.ParserEngineInfo{
			Name:              e.GetName(),
			Description:       e.GetDescription(),
			FileTypes:         e.GetFileTypes(),
			Available:         e.GetAvailable(),
			UnavailableReason: e.GetUnavailableReason(),
		})
	}
	return result, nil
}
