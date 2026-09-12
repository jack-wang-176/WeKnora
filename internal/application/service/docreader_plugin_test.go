package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// stubDocReader records what the builtin reader was asked for, which is how the
// fallback path is observed.
type stubDocReader struct {
	reads       []*types.ReadRequest
	readErr     error
	engines     []types.ParserEngineInfo
	enginesErr  error
	reconnected string
	connected   bool
}

func (r *stubDocReader) Read(_ context.Context, req *types.ReadRequest) (*types.ReadResult, error) {
	r.reads = append(r.reads, req)
	if r.readErr != nil {
		return nil, r.readErr
	}
	return &types.ReadResult{}, nil
}

func (r *stubDocReader) Reconnect(addr string) error { r.reconnected = addr; return nil }
func (r *stubDocReader) IsConnected() bool           { return r.connected }

func (r *stubDocReader) ListEngines(context.Context, map[string]string) ([]types.ParserEngineInfo, error) {
	if r.enginesErr != nil {
		return nil, r.enginesErr
	}
	return r.engines, nil
}

// unreachableChannel hands out a real but lazily-dialled connection to an
// unroutable TEST-NET-1 address, so an RPC fails on the transport without any
// DNS lookup or listening socket.
type unreachableChannel struct {
	*stubChannel
	conn *grpc.ClientConn
}

func (c unreachableChannel) Conn() any { return c.conn }

func newUnreachableChannel(t *testing.T) unreachableChannel {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///192.0.2.10:50051",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient() = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return unreachableChannel{stubChannel: &stubChannel{endpoint: "dns:///192.0.2.10:50051"}, conn: conn}
}

func TestNewPluginDocumentReaderNeedsBothSides(t *testing.T) {
	builtin := &stubDocReader{}
	if got := NewPluginDocumentReader(builtin, nil); got != interfaces.DocumentReader(builtin) {
		t.Fatal("without a host the builtin reader must be returned unchanged")
	}
	if got := NewPluginDocumentReader(nil, newStubHost()); got != nil {
		t.Fatalf("NewPluginDocumentReader(nil builtin) = %v, want nil", got)
	}
	if _, ok := NewPluginDocumentReader(builtin, newStubHost()).(*PluginDocumentReader); !ok {
		t.Fatal("with both sides present the reader must be wrapped")
	}
}

func TestPluginDocumentReaderDelegatesTheConnectionEndpoints(t *testing.T) {
	builtin := &stubDocReader{connected: true}
	r := NewPluginDocumentReader(builtin, newStubHost())
	if err := r.Reconnect("docreader:50051"); err != nil || builtin.reconnected != "docreader:50051" {
		t.Fatalf("Reconnect() = %v, builtin saw %q", err, builtin.reconnected)
	}
	if !r.IsConnected() {
		t.Fatal("IsConnected() = false, want the builtin's answer")
	}
}

// An engine that is not a visible plugin is the builtin reader's business, and
// the host must not be dialled for it.
func TestPluginDocumentReaderReadKeepsBuiltinEngines(t *testing.T) {
	builtin := &stubDocReader{}
	host := newStubHost(manifestOf("mineru--1", extension.KindDocParser))
	r := NewPluginDocumentReader(builtin, host)

	for _, engine := range []string{"", "chain", "mineru--2"} {
		if _, err := r.Read(tenantCtx(1), &types.ReadRequest{ParserEngine: engine}); err != nil {
			t.Fatalf("Read(%q) = %v", engine, err)
		}
	}
	if len(builtin.reads) != 3 {
		t.Fatalf("the builtin reader saw %d reads, want 3", len(builtin.reads))
	}
	if len(host.opened) != 0 {
		t.Fatalf("the host was dialled for a builtin engine: %v", host.opened)
	}
}

// An open failure is not the document's fault either: the read still happens,
// on the builtin engine.
func TestPluginDocumentReaderReadFallsBackWhenTheOpenFails(t *testing.T) {
	builtin := &stubDocReader{}
	host := newStubHost(manifestOf("mineru--1", extension.KindDocParser))
	host.openErr = errors.New("dial refused")
	r := NewPluginDocumentReader(builtin, host)

	if _, err := r.Read(tenantCtx(1), &types.ReadRequest{ParserEngine: "mineru--1"}); err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if len(builtin.reads) != 1 || builtin.reads[0].ParserEngine != "mineru--1" {
		t.Fatalf("the builtin reader saw %+v, want the original request", builtin.reads)
	}
}

// A plugin that is down says nothing about the document, so the read is retried
// on the builtin engine with the plugin's engine name cleared.
func TestPluginDocumentReaderReadFallsBackWhenThePluginIsUnreachable(t *testing.T) {
	builtin := &stubDocReader{}
	host := newStubHost(manifestOf("mineru--1", extension.KindDocParser))
	host.openConn = newUnreachableChannel(t)
	r := NewPluginDocumentReader(builtin, host)

	ctx, cancel := context.WithTimeout(tenantCtx(1), 300*time.Millisecond)
	defer cancel()
	if _, err := r.Read(ctx, &types.ReadRequest{FileName: "a.pdf", ParserEngine: "mineru--1"}); err != nil {
		t.Fatalf("Read() = %v, want the builtin fallback to succeed", err)
	}
	if len(builtin.reads) != 1 {
		t.Fatalf("the builtin reader saw %d reads, want 1", len(builtin.reads))
	}
	if builtin.reads[0].ParserEngine != "" {
		t.Fatalf("fallback request kept ParserEngine %q, want it cleared", builtin.reads[0].ParserEngine)
	}
	if builtin.reads[0].FileName != "a.pdf" {
		t.Fatalf("fallback request lost the document: %+v", builtin.reads[0])
	}
}

// Anything that is not a transport failure is the plugin's verdict on this
// file and must reach the caller unchanged.
func TestPluginDocumentReaderReadPropagatesAPluginVerdict(t *testing.T) {
	builtin := &stubDocReader{}
	host := newStubHost(manifestOf("mineru--1", extension.KindDocParser))
	r := NewPluginDocumentReader(builtin, host)

	// The stub channel carries no connection, so the docparser client refuses
	// the call with a plain error rather than a transport status.
	_, err := r.Read(tenantCtx(1), &types.ReadRequest{ParserEngine: "mineru--1"})
	if err == nil {
		t.Fatal("Read() = nil, want the plugin's error")
	}
	if len(builtin.reads) != 0 {
		t.Fatalf("the builtin reader was asked to retry a business failure: %+v", builtin.reads)
	}
}

func TestPluginUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", status.Error(codes.Unavailable, "connection refused"), true},
		{"deadline", status.Error(codes.DeadlineExceeded, "too slow"), true},
		{"unimplemented", status.Error(codes.Unimplemented, "no Read"), true},
		{"context deadline", context.DeadlineExceeded, true},
		{"wrapped context deadline", errors.Join(errors.New("read"), context.DeadlineExceeded), true},
		{"invalid argument", status.Error(codes.InvalidArgument, "not a pdf"), false},
		{"internal", status.Error(codes.Internal, "parser crashed"), false},
		{"plain error", errors.New("unsupported file type"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pluginUnreachable(tc.err); got != tc.want {
				t.Fatalf("pluginUnreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestPluginDocumentReaderListEnginesAppendsPlugins(t *testing.T) {
	off := manifestOf("off--1", extension.KindDocParser)
	off.Disabled = true
	mineru := manifestOf("mineru--1", extension.KindDocParser)
	mineru.Extension.Capabilities = []string{".pdf"}
	host := newStubHost(
		mineru,
		manifestOf("mineru--2", extension.KindDocParser),
		off,
		// The builtin docreader is served through this host too; it is already
		// in the builtin engine list and must not be listed twice.
		manifestOf(extension.DocreaderExtesnionID, extension.KindDocParser),
	)
	builtin := &stubDocReader{engines: []types.ParserEngineInfo{{Name: "chain"}, {Name: "mineru"}}}
	r := NewPluginDocumentReader(builtin, host)

	got, err := r.ListEngines(tenantCtx(1), nil)
	if err != nil {
		t.Fatalf("ListEngines() = %v", err)
	}
	names := make([]string, 0, len(got))
	for _, e := range got {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "chain,mineru,mineru--1" {
		t.Fatalf("ListEngines() = %v, want the builtins then this tenant's plugins", names)
	}
	if len(got[2].FileTypes) != 1 || got[2].FileTypes[0] != ".pdf" || !got[2].Available {
		t.Fatalf("plugin engine = %+v, want its declared capabilities", got[2])
	}
	if len(host.opened) != 0 {
		t.Fatalf("listing engines dialled %v", host.opened)
	}
}

// A docreader that is down must not hide the plugins that are up.
func TestPluginDocumentReaderListEnginesToleratesABuiltinFailure(t *testing.T) {
	host := newStubHost(manifestOf("mineru--1", extension.KindDocParser))
	builtin := &stubDocReader{enginesErr: errors.New("docreader unreachable")}
	got, err := NewPluginDocumentReader(builtin, host).ListEngines(tenantCtx(1), nil)
	if err != nil {
		t.Fatalf("ListEngines() = %v", err)
	}
	if len(got) != 1 || got[0].Name != "mineru--1" {
		t.Fatalf("ListEngines() = %v, want the plugin alone", got)
	}

	// With nothing to list instead, the builtin failure is the answer.
	_, err = NewPluginDocumentReader(&stubDocReader{
		enginesErr: errors.New("docreader unreachable"),
	}, newStubHost()).ListEngines(tenantCtx(1), nil)
	if err == nil || !strings.Contains(err.Error(), "docreader unreachable") {
		t.Fatalf("ListEngines() = %v, want the builtin failure", err)
	}
}
