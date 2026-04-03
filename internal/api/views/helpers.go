package views

import (
	"fmt"
	"math"
)

func HumanizeBytes(s int64) string {
	sizes := []string{"B", "KB", "MB", "GB", "TB", "PB", "EB"}
	if s < 10 {
		return fmt.Sprintf("%d B", s)
	}
	e := math.Floor(math.Log(float64(s)) / math.Log(1024))
	suffix := sizes[int(e)]
	val := float64(s) / math.Pow(1024, math.Floor(e))
	f := "%.1f"
	if val < 10 {
		f = "%.1f"
	}

	return fmt.Sprintf(f+" %s", val, suffix)
}
