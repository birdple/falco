package unit

import (
	"image"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/birdple/falco/internal/processor"
)

func TestNewPipeline(t *testing.T) {
	p := processor.NewPipeline()
	assert.NotNil(t, p)
	assert.Equal(t, 0, p.Len())
}

func TestPipeline_Add(t *testing.T) {
	p := processor.NewPipeline()

	transform := func(img image.Image) image.Image {
		return img
	}

	p.Add(transform)
	assert.Equal(t, 1, p.Len())

	p.Add(transform)
	assert.Equal(t, 2, p.Len())
}

func TestPipeline_AddIf_True(t *testing.T) {
	p := processor.NewPipeline()

	transform := func(img image.Image) image.Image {
		return img
	}

	p.AddIf(true, transform)
	assert.Equal(t, 1, p.Len())
}

func TestPipeline_AddIf_False(t *testing.T) {
	p := processor.NewPipeline()

	transform := func(img image.Image) image.Image {
		return img
	}

	p.AddIf(false, transform)
	assert.Equal(t, 0, p.Len())
}

func TestPipeline_Execute(t *testing.T) {
	p := processor.NewPipeline()

	// Create a simple test image
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))

	// Add identity transform
	p.Add(func(img image.Image) image.Image {
		return img
	})

	result := p.Execute(img)
	assert.NotNil(t, result)
	assert.Equal(t, img.Bounds(), result.Bounds())
}

func TestPipeline_Execute_Multiple(t *testing.T) {
	p := processor.NewPipeline()

	img := image.NewRGBA(image.Rect(0, 0, 10, 10))

	counter := 0
	transform := func(img image.Image) image.Image {
		counter++
		return img
	}

	p.Add(transform)
	p.Add(transform)
	p.Add(transform)

	p.Execute(img)
	assert.Equal(t, 3, counter)
}

func TestPipeline_Clear(t *testing.T) {
	p := processor.NewPipeline()

	transform := func(img image.Image) image.Image {
		return img
	}

	p.Add(transform)
	p.Add(transform)
	assert.Equal(t, 2, p.Len())

	p.Clear()
	assert.Equal(t, 0, p.Len())
}

func TestPipeline_Chaining(t *testing.T) {
	p := processor.NewPipeline()

	transform := func(img image.Image) image.Image {
		return img
	}

	// Test method chaining
	p.Add(transform).
		AddIf(true, transform).
		AddIf(false, transform).
		Add(transform)

	assert.Equal(t, 3, p.Len())
}

func TestPipeline_Len(t *testing.T) {
	p := processor.NewPipeline()
	assert.Equal(t, 0, p.Len())

	transform := func(img image.Image) image.Image {
		return img
	}

	for i := 1; i <= 5; i++ {
		p.Add(transform)
		assert.Equal(t, i, p.Len())
	}
}
