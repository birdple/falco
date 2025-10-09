package processor

import "image"

// TransformFunc represents a single image transformation function
type TransformFunc func(img image.Image) image.Image

// Pipeline represents a sequence of image transformations
type Pipeline struct {
	transforms []TransformFunc
}

// NewPipeline creates a new transformation pipeline
func NewPipeline() *Pipeline {
	return &Pipeline{
		transforms: make([]TransformFunc, 0),
	}
}

// Add adds a transformation to the pipeline
func (p *Pipeline) Add(transform TransformFunc) *Pipeline {
	p.transforms = append(p.transforms, transform)
	return p
}

// AddIf conditionally adds a transformation to the pipeline
func (p *Pipeline) AddIf(condition bool, transform TransformFunc) *Pipeline {
	if condition {
		p.transforms = append(p.transforms, transform)
	}
	return p
}

// Execute runs all transformations in sequence
func (p *Pipeline) Execute(img image.Image) image.Image {
	result := img
	for _, transform := range p.transforms {
		result = transform(result)
	}
	return result
}

// Clear removes all transformations from the pipeline
func (p *Pipeline) Clear() *Pipeline {
	p.transforms = make([]TransformFunc, 0)
	return p
}

// Len returns the number of transformations in the pipeline
func (p *Pipeline) Len() int {
	return len(p.transforms)
}
