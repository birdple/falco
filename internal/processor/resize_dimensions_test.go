package processor

import "testing"

// TestSafeResizeDimensions pins the box-versus-cap rule, which is the reason
// this function exists and the source of a measured regression: a 150x150
// source asked for at w=600 used to come back 600x600 and 48% heavier.
//
// The rule: giving BOTH dimensions is an exact box and may upscale up to the
// configured maximum; giving ONE is a cap and must never exceed the source's
// native resolution.
func TestSafeResizeDimensions(t *testing.T) {
	p := &VipsProcessor{maxDimensions: struct{ width, height int }{width: 4000, height: 4000}}

	tests := []struct {
		name                 string
		originalW, originalH int
		requestW, requestH   int
		wantW, wantH         int
	}{
		{
			name:      "width-only cap does not upscale past the source",
			originalW: 150, originalH: 150,
			requestW: 600, requestH: 0,
			wantW: 150, wantH: 150,
		},
		{
			name:      "height-only cap does not upscale past the source",
			originalW: 150, originalH: 150,
			requestW: 0, requestH: 600,
			wantW: 150, wantH: 150,
		},
		{
			name:      "explicit box may upscale up to the configured maximum",
			originalW: 150, originalH: 150,
			requestW: 600, requestH: 600,
			wantW: 600, wantH: 600,
		},
		{
			name:      "explicit box is still clamped at the configured maximum",
			originalW: 150, originalH: 150,
			requestW: 10000, requestH: 10000,
			wantW: 4000, wantH: 4000,
		},
		{
			name:      "width-only downscale derives height from the aspect ratio",
			originalW: 1000, originalH: 500,
			requestW: 400, requestH: 0,
			wantW: 400, wantH: 200,
		},
		{
			name:      "height-only downscale derives width from the aspect ratio",
			originalW: 1000, originalH: 500,
			requestW: 0, requestH: 100,
			wantW: 200, wantH: 100,
		},
		{
			name:      "clamping a cap keeps the aspect ratio",
			originalW: 800, originalH: 400,
			requestW: 1600, requestH: 0,
			wantW: 800, wantH: 400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := &ProcessingParams{Width: tt.requestW, Height: tt.requestH}
			gotW, gotH := p.safeResizeDimensions(tt.originalW, tt.originalH, params)
			if gotW != tt.wantW || gotH != tt.wantH {
				t.Errorf("safeResizeDimensions(%d, %d, w=%d h=%d) = %dx%d, want %dx%d",
					tt.originalW, tt.originalH, tt.requestW, tt.requestH, gotW, gotH, tt.wantW, tt.wantH)
			}
		})
	}
}
