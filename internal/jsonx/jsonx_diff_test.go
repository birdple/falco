package jsonx_test

// Test diferencial v1 vs v2.
//
// La regla es simple: todo lo que falco escribe con jsonx.Wire tiene que salir
// byte por byte igual a lo que salía con encoding/json v1. Este archivo es el
// que lo prueba, y es el que se rompe si alguien cambia un tag `omitempty` por
// uno que diverge. Si un caso de aquí falla, NO se ajusta el test: se ajusta el
// tag, porque los bytes son formato de datos persistido en Jay.

import (
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"
	"time"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/jsonx"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// corpusStruct cubre las formas donde v1 y v2 pueden divergir: números, bools,
// punteros, interfaces, slices/mapas nil vs vacíos, strings con caracteres que
// v1 escapa, y time.Time.
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

// diffCases devuelve el corpus completo. Cada valor se marshalea con v1 y con
// v2+Wire y los bytes tienen que coincidir exactamente.
func diffCases() map[string]any {
	stamp := time.Date(2026, 8, 21, 15, 4, 5, 123456789, time.UTC)
	tricky := "a&b <script> \"x\"    ñ 日本語 emoji 🐦"

	return map[string]any{
		// --- Corpus genérico -------------------------------------------------
		"corpus/zero": corpusStruct{},
		"corpus/nil-y-vacio": corpusStruct{
			Slice: []string{}, SliceOmit: []string{},
			Mapa: map[string]int{}, MapaOmit: map[string]int{},
			Bytes: []byte{}, StringMap: map[string]string{},
		},
		"corpus/lleno": corpusStruct{
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
			NumPointer: new(0), // new(expr) de Go 1.27: puntero a un cero explícito
		},

		// --- Escalares sueltos -----------------------------------------------
		"scalar/string-vacio": "",
		"scalar/string-html":  tricky,
		"scalar/nil-slice":    []string(nil),
		"scalar/slice-vacio":  []string{},
		"scalar/nil-map":      map[string]int(nil),
		"scalar/map-vacio":    map[string]int{},
		"scalar/nil-bytes":    []byte(nil),
		"scalar/bytes-vacio":  []byte{},
		"scalar/time-cero":    time.Time{},
		"scalar/time":         stamp,
		"scalar/map-any": map[string]any{
			"n": nil, "s": tricky, "i": 0, "b": false,
			"sl": []any{}, "m": map[string]any{},
		},

		// --- Tipos reales de falco -------------------------------------------
		"falco/ImageMetadata-cero": storage.ImageMetadata{},
		"falco/ImageMetadata-ptr":  &storage.ImageMetadata{},
		"falco/ImageMetadata-lleno": &storage.ImageMetadata{
			ID: "img_1", StorageKey: "k/<a>&b", OriginalName: tricky,
			Format: "webp", Size: 1024, Width: 800, Height: 600,
			ContentType: "image/webp", MaxAge: 31536000, SMaxAge: 0,
			CreatedAt: stamp, ETag: `"abc"`, OwnerID: "owner-1",
		},
		"falco/StorageStats-cero":  storage.StorageStats{},
		"falco/StorageStats-lleno": storage.StorageStats{TotalImages: 3, TotalSize: 9, FreeSpace: 0},

		"falco/UploadResponse-cero": types.UploadResponse{},
		"falco/UploadResponse-lleno": types.UploadResponse{
			Success: true,
			Data: types.UploadData{
				ID: "i", URL: "https://x/?a=1&b=2", OriginalName: tricky,
				Format: "webp", Size: 0, Dimensions: types.Dimensions{}, CreatedAt: stamp,
			},
			Error: &types.APIError{},
		},
		"falco/UpdateResponse-cero":  types.UpdateResponse{},
		"falco/UpdateResponse-vacio": types.UpdateResponse{Updated: []types.UpdateResult{}},
		"falco/UpdateResponse-lleno": types.UpdateResponse{
			Success: true,
			Updated: []types.UpdateResult{{Key: "<k>", Quality: 0, SavedPercent: 0}},
		},
		"falco/ListResponse-cero":  types.ListResponse{},
		"falco/ListResponse-vacio": types.ListResponse{Files: []types.ListItem{}, Directories: []types.DirectoryInfo{}},
		"falco/ListResponse-lleno": types.ListResponse{
			Success: true, Prefix: "p/&", Count: 0,
			Files:       []types.ListItem{{Key: "<k>", Size: 0, Modified: stamp}},
			Directories: []types.DirectoryInfo{{Name: "d", Path: "/d", FileCount: 0}},
		},
		"falco/DeleteResponse-cero":  types.DeleteResponse{},
		"falco/DeleteResponse-vacio": types.DeleteResponse{Deleted: []string{}, Failed: []string{}},
		"falco/DeleteResponse-lleno": types.DeleteResponse{
			Success: false, Deleted: []string{"a"}, Failed: []string{"<b>"},
			Count: 0, Truncated: false,
		},
		"falco/DeleteResponse-truncado": types.DeleteResponse{Truncated: true, Count: 2},

		"falco/UpdateRequest-cero":  types.UpdateRequest{},
		"falco/UpdateRequest-lleno": types.UpdateRequest{URL: "https://x/?a=1&b=2", Quality: 0, Format: "webp"},
		"falco/ListRequest-cero":    types.ListRequest{},
		"falco/DeleteRequest-cero":  types.DeleteRequest{},
		"falco/DeleteRequest-vacio": types.DeleteRequest{Keys: []string{}},

		"falco/SignURLRequest-cero":   handlers.SignURLRequest{},
		"falco/SignURLRequest-lleno":  handlers.SignURLRequest{Path: "/api/v1/images/x?w=1&h=2", ExpiresIn: 0, ExpiresAt: 0},
		"falco/SignURLResponse-cero":  handlers.SignURLResponse{},
		"falco/SignURLResponse-lleno": handlers.SignURLResponse{SignedURL: "https://x/?sig=a&exp=1", Signature: "s", ExpiresAt: 0},

		"falco/JSONResponse-cero":  httputil.JSONResponse{},
		"falco/JSONResponse-data":  httputil.JSONResponse{Success: true, Data: map[string]any{}},
		"falco/JSONResponse-vacio": httputil.JSONResponse{Success: true, Data: ""},
		"falco/JSONResponse-err":   httputil.JSONResponse{Error: &httputil.ErrorInfo{}},

		"falco/ProcessingParams-cero": processor.ProcessingParams{},
		"falco/ProcessingParams-lleno": processor.ProcessingParams{
			Width: 100, Height: 0, Quality: 0, Format: "webp",
			Rotate: 0, Brightness: 0, TrimEnabled: false, AutoOrient: true,
			// WatermarkImage lleva `json:"-"`: acá se comprueba que ni v1 ni
			// v2 lo emiten, que es lo que impide que los bytes del overlay
			// terminen en un log o en una respuesta.
			WatermarkSource: "", WatermarkImage: []byte{1, 2, 3},
		},
	}
}

func TestWireMatchesV1(t *testing.T) {
	for name, val := range diffCases() {
		t.Run(name, func(t *testing.T) {
			v1b, err1 := jsonv1.Marshal(val)
			if err1 != nil {
				t.Fatalf("v1 Marshal falló: %v", err1)
			}
			v2b, err2 := jsonv2.Marshal(val, jsonx.Wire)
			if err2 != nil {
				t.Fatalf("v2 Marshal falló: %v", err2)
			}
			if string(v1b) != string(v2b) {
				t.Fatalf("los bytes divergen\n v1: %s\n v2: %s", v1b, v2b)
			}
		})
	}
}

// TestWireRoundTripsImageMetadata cubre el camino de vuelta: lo que v1 escribió
// alguna vez (y sigue guardado en Jay) tiene que poder leerse con v2, y lo que
// v2 escribe tiene que poder leerse con v1. ImageMetadata es el caso delicado
// porque tiene MarshalJSON/UnmarshalJSON propios con el truco de `type Alias`.
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
		t.Fatalf("los bytes divergen\n v1: %s\n v2: %s", v1b, v2b)
	}

	// v2 lee lo que escribió v1.
	var fromV1 storage.ImageMetadata
	if err := jsonv2.Unmarshal(v1b, &fromV1, jsonx.Wire); err != nil {
		t.Fatalf("v2 Unmarshal de bytes v1: %v", err)
	}
	if fromV1 != orig {
		t.Fatalf("round-trip v1→v2 perdió datos\n esperado: %+v\n obtenido: %+v", orig, fromV1)
	}

	// v1 lee lo que escribió v2.
	var fromV2 storage.ImageMetadata
	if err := jsonv1.Unmarshal(v2b, &fromV2); err != nil {
		t.Fatalf("v1 Unmarshal de bytes v2: %v", err)
	}
	if fromV2 != orig {
		t.Fatalf("round-trip v2→v1 perdió datos\n esperado: %+v\n obtenido: %+v", orig, fromV2)
	}
}
