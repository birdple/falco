package handlers

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cshum/vipsgen/vips"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

const probeField = "exif-ifd0-ImageDescription"

// jpegWithEXIF encodes a small JPEG that carries an EXIF field, standing in
// for the GPS block a phone camera writes.
func jpegWithEXIF(t *testing.T) []byte {
	t.Helper()
	img, err := vips.NewBlack(32, 32, &vips.BlackOptions{Bands: 3})
	require.NoError(t, err)
	defer img.Close()
	img.SetString(probeField, "camera gps probe")
	data, err := img.JpegsaveBuffer(&vips.JpegsaveBufferOptions{Q: 90, Keep: vips.KeepAll})
	require.NoError(t, err)
	return data
}

func hasEXIFProbe(t *testing.T, data []byte) bool {
	t.Helper()
	img, err := vips.NewImageFromBuffer(data, nil)
	require.NoError(t, err)
	defer img.Close()
	_, err = img.GetString(probeField)
	return err == nil
}

// TestUpload_StripsMetadataFromStoredOriginal guards the regression where the
// upload path built ProcessingParams without the strip flag and every stored
// original kept its EXIF — GPS included — which raw delivery then streamed.
func TestUpload_StripsMetadataFromStoredOriginal(t *testing.T) {
	src := jpegWithEXIF(t)
	require.True(t, hasEXIFProbe(t, src), "test fixture must carry the probe")

	fs, err := storage.NewFilesystemStorage(t.TempDir())
	require.NoError(t, err)
	cfg := &config.Config{}
	cfg.Storage.Default = "default"
	cfg.Processing.MaxFileSizeMB = 10
	proc := processor.NewVipsProcessor(10, 85, processor.FormatWebP, 4096, 4096)
	h := NewHandler(cfg, fs, proc, time.Now())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload?format=jpeg&id=probe", bytes.NewReader(src))
	req.Header.Set("Content-Type", "image/jpeg")
	w := httptest.NewRecorder()
	h.HandleUpload(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	body, _, err := fs.Retrieve(req.Context(), "probe")
	require.NoError(t, err)
	defer func() { _ = body.Close() }()
	stored, err := io.ReadAll(body)
	require.NoError(t, err)

	require.False(t, hasEXIFProbe(t, stored), "the stored original still carries EXIF metadata")
}
