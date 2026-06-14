package render

import (
	"image"
	"image/color"
	"image/draw"
	"os"
	"time"

	_ "image/png"

	_ "golang.org/x/image/webp"

	"codex-pets/internal/catalog"
)

// AnimationState describes one spritesheet row. The table mirrors
// PetModels.swift so both hosts animate identically.
type AnimationState struct {
	ID            string
	Row           int
	Frames        int
	FrameDuration time.Duration
}

var animationStates = []AnimationState{
	{ID: "idle", Row: 0, Frames: 6, FrameDuration: 160 * time.Millisecond},
	{ID: "running-right", Row: 1, Frames: 8, FrameDuration: 120 * time.Millisecond},
	{ID: "running-left", Row: 2, Frames: 8, FrameDuration: 120 * time.Millisecond},
	{ID: "waving", Row: 3, Frames: 4, FrameDuration: 140 * time.Millisecond},
	{ID: "jumping", Row: 4, Frames: 5, FrameDuration: 140 * time.Millisecond},
	{ID: "failed", Row: 5, Frames: 8, FrameDuration: 140 * time.Millisecond},
	{ID: "waiting", Row: 6, Frames: 6, FrameDuration: 150 * time.Millisecond},
	{ID: "running", Row: 7, Frames: 6, FrameDuration: 120 * time.Millisecond},
	{ID: "review", Row: 8, Frames: 6, FrameDuration: 150 * time.Millisecond},
}

// StateByID falls back to idle for unknown state ids.
func StateByID(id string) AnimationState {
	for _, state := range animationStates {
		if state.ID == id {
			return state
		}
	}
	return animationStates[0]
}

type frameKey struct {
	stateID string
	index   int
	width   int
	height  int
}

// SpriteSheet slices a pet atlas into animation frames. Decoding supports
// PNG and WebP, covering every prebundled pet.
type SpriteSheet struct {
	atlas       *image.NRGBA
	frameWidth  int
	frameHeight int
	cache       map[frameKey]*image.NRGBA
}

// LoadSpriteSheet loads the pet package at petDir through the shared catalog
// validation (manifest limits, MIME checks, path traversal).
func LoadSpriteSheet(petDir string) (*SpriteSheet, error) {
	pet, err := catalog.LoadLocalPet(petDir, catalog.DefaultLimits())
	if err != nil {
		return nil, err
	}
	file, err := os.Open(pet.SourcePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoded, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}
	atlas := image.NewNRGBA(image.Rect(0, 0, decoded.Bounds().Dx(), decoded.Bounds().Dy()))
	draw.Draw(atlas, atlas.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	return &SpriteSheet{
		atlas:       atlas,
		frameWidth:  pet.FrameWidth,
		frameHeight: pet.FrameHeight,
		cache:       map[frameKey]*image.NRGBA{},
	}, nil
}

// Frame returns the animation frame scaled to width x height. Out-of-range
// rows fall back to the idle row and frame indexes wrap around the columns
// the atlas actually has, so a layout mismatch can never produce a partial,
// stretched frame.
func (s *SpriteSheet) Frame(stateID string, index int, width int, height int) *image.NRGBA {
	state := StateByID(stateID)
	columns := s.atlas.Bounds().Dx() / s.frameWidth
	rows := s.atlas.Bounds().Dy() / s.frameHeight
	if columns < 1 || rows < 1 {
		return placeholderFrame(width, height)
	}
	row := state.Row
	if row >= rows {
		row = 0
	}
	frames := state.Frames
	if frames > columns {
		frames = columns
	}
	index = index % frames
	if index < 0 {
		index += frames
	}

	key := frameKey{stateID: state.ID, index: index, width: width, height: height}
	if cached, ok := s.cache[key]; ok {
		return cached
	}
	source := image.Rect(
		index*s.frameWidth,
		row*s.frameHeight,
		(index+1)*s.frameWidth,
		(row+1)*s.frameHeight,
	)
	frame := scaleNearest(s.atlas, source, width, height)
	s.cache[key] = frame
	return frame
}

func placeholderFrame(width int, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.NRGBA{R: 240, G: 244, B: 248, A: 255}), image.Point{}, draw.Src)
	return img
}

func scaleNearest(src *image.NRGBA, source image.Rectangle, width int, height int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		sy := source.Min.Y + y*source.Dy()/height
		srcRow := src.PixOffset(source.Min.X, sy)
		dstRow := dst.PixOffset(0, y)
		for x := 0; x < width; x++ {
			sx := x * source.Dx() / width
			copy(dst.Pix[dstRow+x*4:dstRow+x*4+4], src.Pix[srcRow+sx*4:srcRow+sx*4+4])
		}
	}
	return dst
}
