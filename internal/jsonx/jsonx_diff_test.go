package jsonx_test

// Differential test, v1 vs v2.
//
// The rule is simple: everything falco writes with jsonx.Wire must come out
// byte for byte identical to what encoding/json v1 produced. This file is what
// proves it, and what breaks if someone swaps an `omitempty` tag for one that
// diverges. If a case here fails, do NOT adjust the test: adjust the tag,
// because the bytes are a data format persisted in Jay.

import (
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"
	"time"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/jsonx"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// corpusStruct covers the shapes where v1 and v2 can diverge: numbers, bools,
// pointers, interfaces, nil vs empty slices/maps, strings with characters that
// v1 escapes, and time.Time.
type corpusStruct struct {
	Str        string            `json:"str"`
	StrOmit    string            `json:"str_omit,omitempty"`
	Num        int               `json:"num"`
	NumOmit    int               `json:"num_omit,omitzero"`
	Flt        float64           `json:"flt"`
	FltOmit    float64           `json:"flt_omit,omitzero"`
	Boolean    bool              `json:"boolean"`
	BoolOmit   bool              `json:"bool_omit,omitzero"`
	Slice      []string          `json:"slice"`
	SliceOmit  []string          `json:"slice_omit,omitempty"`
	Bytes      []byte            `json:"bytes"`
	Mapa       map[string]int    `json:"mapa"`
	MapaOmit   map[string]int    `json:"mapa_omit,omitempty"`
	Ptr        *innerStruct      `json:"ptr"`
	PtrOmit    *innerStruct      `json:"ptr_omit,omitzero"`
	Iface      any               `json:"iface"`
	IfaceOmit  any               `json:"iface_omit,omitzero"`
	Nested     innerStruct       `json:"nested"`
	Stamp      time.Time         `json:"stamp"`
	StampOmit  time.Time         `json:"stamp_omit,omitzero"`
	StringMap  map[string]string `json:"string_map"`
	NumPointer *int              `json:"num_pointer,omitzero"`
}

type innerStruct struct {
	A string `json:"a"`
	B int    `json:"b"`
}

// diffCases returns the full corpus. Each value is marshalled with v1 and with
// v2+Wire, and the bytes must match exactly.
func diffCases() map[string]any {
	stamp := time.Date(2026, 8, 21, 15, 4, 5, 123456789, time.UTC)
	zeroCount := 0
	tricky := "a&b <script> \"x\"    ñ 日本語 emoji 🐦"

	return map[string]any{
		// --- Generic corpus --------------------------------------------------
		"corpus/zero": corpusStruct{},
		"corpus/nil-and-empty": corpusStruct{
			Slice: []string{}, SliceOmit: []string{},
			Mapa: map[string]int{}, MapaOmit: map[string]int{},
			Bytes: []byte{}, StringMap: map[string]string{},
		},
		"corpus/full": corpusStruct{
			Str: tricky, StrOmit: tricky,
			Num: -7, NumOmit: 7, Flt: -1.5, FltOmit: 2.25,
			Boolean: true, BoolOmit: true,
			Slice: []string{"a", tricky}, SliceOmit: []string{""},
			Bytes:    []byte("<&>"),
			Mapa:     map[string]int{"b": 2, "a": 1},
			MapaOmit: map[string]int{"z": 26, "a": 1},
			Ptr:      &innerStruct{}, PtrOmit: &innerStruct{A: tricky, B: 3},
			Iface: "", IfaceOmit: map[string]any{"k": tricky},
			Nested:     innerStruct{A: tricky, B: -1},
			Stamp:      stamp,
			StampOmit:  stamp,
			StringMap:  map[string]string{"<k>": "&v"},
			NumPointer: new(0), // Go 1.27 new(expr): pointer to an explicit zero
		},

		// --- Standalone scalars ----------------------------------------------
		"scalar/string-empty": "",
		"scalar/string-html":  tricky,
		"scalar/nil-slice":    []string(nil),
		"scalar/slice-empty":  []string{},
		"scalar/nil-map":      map[string]int(nil),
		"scalar/map-empty":    map[string]int{},
		"scalar/nil-bytes":    []byte(nil),
		"scalar/bytes-empty":  []byte{},
		"scalar/time-zero":    time.Time{},
		"scalar/time":         stamp,
		"scalar/map-any": map[string]any{
			"n": nil, "s": tricky, "i": 0, "b": false,
			"sl": []any{}, "m": map[string]any{},
		},

		// --- Real falco types ------------------------------------------------
		"falco/ImageMetadata-zero": storage.ImageMetadata{},
		"falco/ImageMetadata-ptr":  &storage.ImageMetadata{},
		"falco/ImageMetadata-full": &storage.ImageMetadata{
			ID: "img_1", StorageKey: "k/<a>&b", OriginalName: tricky,
			Format: "webp", Size: 1024, Width: 800, Height: 600,
			ContentType: "image/webp", MaxAge: 31536000, SMaxAge: 0,
			CreatedAt: stamp, ETag: `"abc"`, OwnerID: "owner-1",
		},
		"falco/StorageStats-zero": storage.StorageStats{},
		"falco/StorageStats-full": storage.StorageStats{TotalImages: 3, TotalSize: 9, FreeSpace: 0},

		"falco/UploadResponse-zero": types.UploadResponse{},
		"falco/UploadResponse-full": types.UploadResponse{
			Success: true,
			Data: types.UploadData{
				ID: "i", URL: "https://x/?a=1&b=2", OriginalName: tricky,
				Format: "webp", Size: 0, Dimensions: types.Dimensions{}, CreatedAt: stamp,
			},
		},
		"falco/ErrorResponse-zero":   types.ErrorResponse{},
		"falco/ErrorResponse-full":   types.ErrorResponse{Error: &types.APIError{Code: "C", Message: tricky}},
		"falco/UpdateResponse-zero":  types.UpdateResponse{},
		"falco/UpdateResponse-empty": types.UpdateResponse{Updated: []types.UpdateResult{}},
		"falco/UpdateResponse-full": types.UpdateResponse{
			Success: true,
			Updated: []types.UpdateResult{{Key: "<k>", Quality: 0, SavedPercent: 0}},
		},
		"falco/ListResponse-zero":  types.ListResponse{},
		"falco/ListResponse-empty": types.ListResponse{Files: []types.ListItem{}, Directories: []types.DirectoryInfo{}},
		"falco/ListResponse-full": types.ListResponse{
			Success: true, Prefix: "p/&", Count: 0,
			Files:       []types.ListItem{{Key: "<k>", Size: 0, Modified: stamp}},
			Directories: []types.DirectoryInfo{{Name: "d", Path: "/d", FileCount: &zeroCount}},
		},
		// Paginated listings carry no file count: the pointer is nil and has to
		// serialise as null in both v1 and v2, not vanish or turn into a 0.
		"falco/ListResponse-paginated": types.ListResponse{
			Success: true, Prefix: "p/", Count: 1, Truncated: true, NextCursor: "p/a&b",
			Files:       []types.ListItem{{Key: "a", Size: 1, Modified: stamp, ContentType: "image/webp", ETag: "e"}},
			Directories: []types.DirectoryInfo{{Name: "d", Path: "p/d", FileCount: nil}},
		},
		"falco/DeleteResponse-zero":  types.DeleteResponse{},
		"falco/DeleteResponse-empty": types.DeleteResponse{Deleted: []string{}, Failed: []string{}},
		"falco/DeleteResponse-full": types.DeleteResponse{
			Success: false, Deleted: []string{"a"}, Failed: []string{"<b>"},
			Count: 0, Truncated: false,
		},
		"falco/DeleteResponse-truncated": types.DeleteResponse{Truncated: true, Count: 2},

		"falco/UpdateRequest-zero":  types.UpdateRequest{},
		"falco/UpdateRequest-full":  types.UpdateRequest{URL: "https://x/?a=1&b=2", Quality: 0, Format: "webp"},
		"falco/DeleteRequest-zero":  types.DeleteRequest{},
		"falco/DeleteRequest-empty": types.DeleteRequest{Keys: []string{}},

		"falco/SignURLRequest-zero":  handlers.SignURLRequest{},
		"falco/SignURLRequest-full":  handlers.SignURLRequest{Path: "/api/v1/images/x?w=1&h=2", ExpiresIn: 0, ExpiresAt: 0},
		"falco/SignURLResponse-zero": handlers.SignURLResponse{},
		"falco/SignURLResponse-full": handlers.SignURLResponse{SignedURL: "https://x/?sig=a&exp=1", Signature: "s", ExpiresAt: 0},

		"falco/ProcessingParams-zero": processor.ProcessingParams{},
		"falco/ProcessingParams-full": processor.ProcessingParams{
			Width: 100, Height: 0, Quality: 0, Format: "webp",
			Rotate: 0, Brightness: 0, TrimEnabled: false, SkipAutoOrient: true,
			// WatermarkImage carries `json:"-"`: this checks that neither v1
			// nor v2 emits it, which is what keeps the overlay bytes out of a
			// log or a response.
			WatermarkSource: "", WatermarkImage: []byte{1, 2, 3},
		},
	}
}

func TestWireMatchesV1(t *testing.T) {
	for name, val := range diffCases() {
		t.Run(name, func(t *testing.T) {
			v1b, err1 := jsonv1.Marshal(val)
			if err1 != nil {
				t.Fatalf("v1 Marshal failed: %v", err1)
			}
			v2b, err2 := jsonv2.Marshal(val, jsonx.Wire)
			if err2 != nil {
				t.Fatalf("v2 Marshal failed: %v", err2)
			}
			if string(v1b) != string(v2b) {
				t.Fatalf("bytes diverge\n v1: %s\n v2: %s", v1b, v2b)
			}
		})
	}
}

// TestWireRoundTripsImageMetadata covers the way back: whatever v1 ever wrote
// (and is still stored in Jay) must be readable with v2, and whatever v2 writes
// must be readable with v1. ImageMetadata is the delicate case because it has
// its own MarshalJSON/UnmarshalJSON using the `type Alias` trick.
func TestWireRoundTripsImageMetadata(t *testing.T) {
	orig := storage.ImageMetadata{
		ID: "img_1", StorageKey: "k/<a>&b", OriginalName: "a&b <x> ñ 🐦",
		Format: "webp", Size: 1024, Width: 800, Height: 600,
		ContentType: "image/webp", MaxAge: 31536000, SMaxAge: 7200,
		CreatedAt: time.Date(2026, 8, 21, 15, 4, 5, 0, time.UTC),
		ETag:      `"abc"`, OwnerID: "owner-1",
	}

	v1b, err := jsonv1.Marshal(&orig)
	if err != nil {
		t.Fatalf("v1 Marshal: %v", err)
	}
	v2b, err := jsonv2.Marshal(&orig, jsonx.Wire)
	if err != nil {
		t.Fatalf("v2 Marshal: %v", err)
	}
	if string(v1b) != string(v2b) {
		t.Fatalf("bytes diverge\n v1: %s\n v2: %s", v1b, v2b)
	}

	// v2 reads what v1 wrote.
	var fromV1 storage.ImageMetadata
	if err := jsonv2.Unmarshal(v1b, &fromV1, jsonx.Wire); err != nil {
		t.Fatalf("v2 Unmarshal of v1 bytes: %v", err)
	}
	if fromV1 != orig {
		t.Fatalf("round-trip v1→v2 lost data\n want: %+v\n got: %+v", orig, fromV1)
	}

	// v1 reads what v2 wrote.
	var fromV2 storage.ImageMetadata
	if err := jsonv1.Unmarshal(v2b, &fromV2); err != nil {
		t.Fatalf("v1 Unmarshal of v2 bytes: %v", err)
	}
	if fromV2 != orig {
		t.Fatalf("round-trip v2→v1 lost data\n want: %+v\n got: %+v", orig, fromV2)
	}
}
