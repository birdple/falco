package processor

import "testing"

// TestWatermarkOrigin covers the placement arithmetic, which is the part that
// does not need libvips: where each named position puts the overlay's top-left
// corner, and what happens when the overlay does not fit.
func TestWatermarkOrigin(t *testing.T) {
	// A 400-wide base gives a margin of 8 (2%), which is above the floor.
	const baseW, baseH = 400, 300
	const overlayW, overlayH = 80, 60

	cases := []struct {
		name     string
		position string
		wantX    int
		wantY    int
	}{
		{"por omisión abajo a la derecha", "", baseW - overlayW - 8, baseH - overlayH - 8},
		{"abajo a la derecha", WatermarkBottomRight, baseW - overlayW - 8, baseH - overlayH - 8},
		{"arriba a la izquierda", WatermarkTopLeft, 8, 8},
		{"arriba a la derecha", WatermarkTopRight, baseW - overlayW - 8, 8},
		{"abajo a la izquierda", WatermarkBottomLeft, 8, baseH - overlayH - 8},
		{"centrado", WatermarkCenter, (baseW - overlayW) / 2, (baseH - overlayH) / 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, y := watermarkOrigin(tc.position, baseW, baseH, overlayW, overlayH)
			if x != tc.wantX || y != tc.wantY {
				t.Fatalf("origen = (%d,%d), se esperaba (%d,%d)", x, y, tc.wantX, tc.wantY)
			}
		})
	}
}

// TestWatermarkOriginClampsToZero: an overlay wider than the image would give a
// negative origin, which vips reads as a crop of the overlay rather than as an
// error — so the logo would come out with its own left edge cut off.
func TestWatermarkOriginClampsToZero(t *testing.T) {
	x, y := watermarkOrigin(WatermarkBottomRight, 100, 100, 200, 200)
	if x != 0 || y != 0 {
		t.Fatalf("origen = (%d,%d), se esperaba (0,0)", x, y)
	}
}

// TestWatermarkMarginFloor: on a small render 2% of the width rounds down to
// nothing, and a watermark flush against the edge reads as a rendering bug.
func TestWatermarkMarginFloor(t *testing.T) {
	x, _ := watermarkOrigin(WatermarkTopLeft, 50, 50, 10, 10)
	if x != minWatermarkMargin {
		t.Fatalf("margen = %d, se esperaba el piso de %d", x, minWatermarkMargin)
	}
}

// TestIsValidWatermarkPosition guards the allowlist the query parser uses: an
// unknown position falls back to the default rather than failing the request,
// so the check has to be the thing that recognises a good one.
func TestIsValidWatermarkPosition(t *testing.T) {
	valid := []string{
		WatermarkTopLeft, WatermarkTopRight, WatermarkBottomLeft,
		WatermarkBottomRight, WatermarkCenter,
	}
	for _, position := range valid {
		if !IsValidWatermarkPosition(position) {
			t.Errorf("%q debería ser válida", position)
		}
	}

	for _, position := range []string{"", "diagonal", "TOP-LEFT", "north"} {
		if IsValidWatermarkPosition(position) {
			t.Errorf("%q no debería ser válida", position)
		}
	}
}

// TestNormalizeAngle: -90 and 270 are the same quarter turn, and only the
// normalised form takes the exact vips_rot branch instead of the interpolating
// one that comes out a pixel short.
func TestNormalizeAngle(t *testing.T) {
	cases := map[float64]float64{
		0: 0, 90: 90, 180: 180, 270: 270,
		-90: 270, -180: 180, -270: 90,
		360: 0, 450: 90, -450: 270,
		45: 45, -45: 315,
	}

	for input, want := range cases {
		if got := normalizeAngle(input); got != want {
			t.Errorf("normalizeAngle(%v) = %v, se esperaba %v", input, got, want)
		}
	}
}
