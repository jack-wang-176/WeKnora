package plugin

import (
	"testing"
	"time"

	datasourcev1 "github.com/Tencent/WeKnora/docreader/proto/plugin/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestToPBStruct(t *testing.T) {
	if s, err := toPBStruct(nil, "credentials"); s != nil || err != nil {
		t.Fatalf("toPBStruct(nil) = (%v,%v), want (nil,nil)", s, err)
	}
	s, err := toPBStruct(map[string]interface{}{"token": "t", "n": 1.0}, "credentials")
	if err != nil {
		t.Fatalf("toPBStruct() = %v", err)
	}
	if got := s.AsMap(); got["token"] != "t" || got["n"] != 1.0 {
		t.Fatalf("toPBStruct() = %v, want the map round-tripped", got)
	}
	// A value protobuf cannot carry is refused here, where the field name is
	// still known, rather than dropped on the wire.
	if _, err := toPBStruct(map[string]interface{}{"ch": make(chan int)}, "settings"); err == nil {
		t.Fatal("toPBStruct(unencodable) = nil, want an error naming the field")
	} else if !contains(err.Error(), "settings") {
		t.Fatalf("toPBStruct() error = %v, want it to name the field", err)
	}
}

func TestToPBConfig(t *testing.T) {
	if c, err := toPBConfig(nil); c != nil || err != nil {
		t.Fatalf("toPBConfig(nil) = (%v,%v), want (nil,nil)", c, err)
	}
	got, err := toPBConfig(&types.DataSourceConfig{
		Type:              "notion--1",
		Credentials:       map[string]interface{}{"token": "t"},
		ResourceIDs:       []string{"r1", "r2"},
		Settings:          map[string]interface{}{"depth": 2.0},
		MultimodalEnabled: true,
	})
	if err != nil {
		t.Fatalf("toPBConfig() = %v", err)
	}
	if got.GetType() != "notion--1" || !got.GetMultimodalEnabled() ||
		len(got.GetResourceIds()) != 2 ||
		got.GetCredentials().AsMap()["token"] != "t" ||
		got.GetSettings().AsMap()["depth"] != 2.0 {
		t.Fatalf("toPBConfig() = %+v, want every field carried over", got)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	if c, err := toPBCursor(nil); c != nil || err != nil {
		t.Fatalf("toPBCursor(nil) = (%v,%v), want (nil,nil)", c, err)
	}
	if got := fromPBCursor(nil); got != nil {
		t.Fatalf("fromPBCursor(nil) = %v, want nil", got)
	}

	at := time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC)
	in := &types.SyncCursor{
		LastSyncTime:    at,
		ConnectorCursor: map[string]interface{}{"page": "2"},
		LastSchemaHash:  "h1",
	}
	pb, err := toPBCursor(in)
	if err != nil {
		t.Fatalf("toPBCursor() = %v", err)
	}
	out := fromPBCursor(pb)
	if !out.LastSyncTime.Equal(at) || out.LastSchemaHash != "h1" || out.ConnectorCursor["page"] != "2" {
		t.Fatalf("cursor round trip = %+v, want %+v", out, in)
	}

	// A zero time carries no timestamp, so a first sync is not reported as
	// having happened at the unix epoch.
	zero, err := toPBCursor(&types.SyncCursor{LastSchemaHash: "h1"})
	if err != nil {
		t.Fatalf("toPBCursor(zero time) = %v", err)
	}
	if zero.GetLastSyncTime() != nil {
		t.Fatalf("toPBCursor(zero time).LastSyncTime = %v, want nil", zero.GetLastSyncTime())
	}
}

func TestFromPBResource(t *testing.T) {
	at := time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC)
	got := fromPBResource(&datasourcev1.Resource{
		ExternalId:  "r1",
		Name:        "Roadmap",
		Type:        "page",
		Description: "d",
		Url:         "https://example.com/r1",
		ModifiedAt:  timestamppb.New(at),
		ParentId:    "r0",
		HasChildren: true,
		Metadata:    map[string]string{"space": "eng"},
	})
	if got.ExternalID != "r1" || got.Name != "Roadmap" || got.Type != "page" ||
		got.ParentID != "r0" || !got.HasChildren || !got.ModifiedAt.Equal(at) ||
		got.Metadata["space"] != "eng" {
		t.Fatalf("fromPBResource() = %+v, want every field carried over", got)
	}
	if empty := fromPBResource(&datasourcev1.Resource{ExternalId: "r1"}); empty.Metadata != nil {
		t.Fatalf("fromPBResource() left an empty metadata map: %v", empty.Metadata)
	}
}

func TestFromPBItem(t *testing.T) {
	at := time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC)
	got := fromPBItem(&datasourcev1.FetchedItem{
		ExternalId:       "i1",
		Title:            "T",
		Content:          []byte("C"),
		ContentType:      "text/markdown",
		FileName:         "f.md",
		Url:              "https://example.com/i1",
		UpdatedAt:        timestamppb.New(at),
		Metadata:         map[string]string{"k": "v"},
		IsDeleted:        true,
		SourceResourceId: "r1",
		ReplacesSubtree:  true,
		SubtreeKeep:      []string{"i2"},
	})
	if got.ExternalID != "i1" || got.Title != "T" || string(got.Content) != "C" ||
		got.ContentType != "text/markdown" || got.FileName != "f.md" ||
		!got.UpdatedAt.Equal(at) || got.Metadata["k"] != "v" || !got.IsDeleted ||
		got.SourceResourceID != "r1" || !got.ReplacesSubtree || len(got.SubtreeKeep) != 1 {
		t.Fatalf("fromPBItem() = %+v, want every field carried over", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return len(sub) == 0
}
