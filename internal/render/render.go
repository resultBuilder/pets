package render

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"

	"golang.org/x/image/font"

	"codex-pets/internal/protocol"
)

// Logical layout units; every metric is multiplied by the renderer scale.
const (
	DefaultScale = 0.76
	MinScale     = 0.58
	MaxScale     = 3

	LogicalWidth  = 190
	LogicalHeight = 235

	spriteScaleFactor   = 0.72
	spriteDefaultWidth  = 192
	spriteDefaultHeight = 208
	spriteBottomInset   = 18
	spriteLogicalWidth  = 138
	spriteLogicalHeight = 150
	spriteLogicalTop    = LogicalHeight - spriteLogicalHeight - spriteBottomInset
)

// UpdateAction is what a click on the update pill should trigger.
type UpdateAction string

const (
	UpdateActionNone    UpdateAction = ""
	UpdateActionApply   UpdateAction = "apply"
	UpdateActionDismiss UpdateAction = "dismiss"
)

// Input is the renderer-agnostic description of one frame.
type Input struct {
	StateID           string
	Bubble            string
	PendingApprovalID string
	ActiveSessions    int
	Update            *protocol.UpdateState
	FrameIndex        int
}

// Regions reports interactive areas in buffer coordinates. Everything
// outside them is click-through for the windowing layer. Visible lists every
// painted area so non-composited hosts can shape the window outline to it.
type Regions struct {
	Body            image.Rectangle
	UpdatePill      image.Rectangle
	UpdateAction    UpdateAction
	ApprovalID      string
	ApprovalApprove image.Rectangle
	ApprovalDeny    image.Rectangle
	Visible         []image.Rectangle
}

// Equal reports whether two region sets describe the same geometry.
func (r Regions) Equal(other Regions) bool {
	if r.Body != other.Body ||
		r.UpdatePill != other.UpdatePill ||
		r.UpdateAction != other.UpdateAction ||
		r.ApprovalID != other.ApprovalID ||
		r.ApprovalApprove != other.ApprovalApprove ||
		r.ApprovalDeny != other.ApprovalDeny {
		return false
	}
	if len(r.Visible) != len(other.Visible) {
		return false
	}
	for index := range r.Visible {
		if r.Visible[index] != other.Visible[index] {
			return false
		}
	}
	return true
}

// Renderer composes complete overlay frames into a reusable NRGBA buffer.
// It is not safe for concurrent use; the host run loop owns it.
type Renderer struct {
	scale float64
	sheet *SpriteSheet
	buf   *image.NRGBA
	faces map[int]font.Face
}

func NewRenderer(scale float64) *Renderer {
	if scale <= 0 {
		scale = DefaultScale
	}
	if scale < MinScale {
		scale = MinScale
	}
	if scale > MaxScale {
		scale = MaxScale
	}
	return &Renderer{
		scale: scale,
		faces: map[int]font.Face{},
	}
}

func (r *Renderer) Size() (int, int) {
	width := max(LogicalWidth, int(170*r.scale+50+0.5))
	height := max(LogicalHeight, int(190*r.scale+80+0.5))
	return width, height
}

func (r *Renderer) SetSheet(sheet *SpriteSheet) {
	r.sheet = sheet
}

func (r *Renderer) HasPet() bool {
	return r.sheet != nil
}

func (r *Renderer) s(value int) int {
	return int(float64(value)*r.scale + 0.5)
}

func (r *Renderer) face(logicalSize int) font.Face {
	size := r.s(logicalSize)
	if face, ok := r.faces[size]; ok {
		return face
	}
	face := newFace(size)
	if face != nil {
		r.faces[size] = face
	}
	return face
}

// Compose renders one full window frame over a transparent background and
// returns it together with the interactive regions.
func (r *Renderer) Compose(in Input) (*image.NRGBA, Regions) {
	width, height := r.Size()
	if r.buf == nil || r.buf.Bounds().Dx() != width || r.buf.Bounds().Dy() != height {
		r.buf = image.NewNRGBA(image.Rect(0, 0, width, height))
	}
	clear(r.buf.Pix)

	spriteWidth, spriteHeight := r.spriteSize()
	spriteRect := image.Rect(0, 0, spriteWidth, spriteHeight).
		Add(image.Pt((width-spriteWidth)/2, height-spriteHeight-spriteBottomInset))
	if r.sheet != nil {
		frame := r.sheet.Frame(in.StateID, in.FrameIndex, spriteRect.Dx(), spriteRect.Dy())
		draw.Draw(r.buf, spriteRect, frame, image.Point{}, draw.Over)
	} else {
		r.drawMissingPetCard(spriteRect)
	}

	regions := Regions{
		Body:    clampRect(spriteRect.Inset(-r.s(12)), r.buf.Bounds()),
		Visible: []image.Rectangle{spriteRect},
	}

	if in.Bubble != "" {
		rect, approve, deny := r.drawBubble(in.Bubble, width, in.PendingApprovalID != "")
		if !rect.Empty() {
			regions.Visible = append(regions.Visible, rect)
			if in.PendingApprovalID != "" && !approve.Empty() && !deny.Empty() {
				regions.ApprovalID = in.PendingApprovalID
				regions.ApprovalApprove = approve
				regions.ApprovalDeny = deny
			}
		}
	}

	bottom := height - r.s(8)
	if pillText, action, pillColor := updatePill(in.Update); pillText != "" {
		rect := r.drawChip(pillText, 11, width, bottom, pillColor, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		bottom = rect.Min.Y - r.s(4)
		regions.UpdatePill = rect
		regions.UpdateAction = action
		regions.Visible = append(regions.Visible, rect)
	}
	if in.ActiveSessions > 0 {
		rect := r.drawChip(sessionText(in.ActiveSessions), 10, width, bottom,
			color.NRGBA{R: 40, G: 44, B: 52, A: 200}, color.NRGBA{R: 235, G: 240, B: 245, A: 255})
		if !rect.Empty() {
			regions.Visible = append(regions.Visible, rect)
		}
	}
	return r.buf, regions
}

func (r *Renderer) spriteSize() (int, int) {
	frameWidth := spriteDefaultWidth
	frameHeight := spriteDefaultHeight
	if r.sheet != nil {
		frameWidth = r.sheet.frameWidth
		frameHeight = r.sheet.frameHeight
	}
	return int(float64(frameWidth)*spriteScaleFactor*r.scale + 0.5),
		int(float64(frameHeight)*spriteScaleFactor*r.scale + 0.5)
}

func (r *Renderer) drawMissingPetCard(rect image.Rectangle) {
	fillRoundedRect(r.buf, rect, r.s(14), color.NRGBA{R: 236, G: 240, B: 246, A: 235})
	face := r.face(14)
	if face == nil {
		return
	}
	title := "Pi Pet"
	titleX := rect.Min.X + (rect.Dx()-textWidth(face, title))/2
	titleY := rect.Min.Y + rect.Dy()/2
	drawText(r.buf, face, titleX, titleY, title, color.NRGBA{R: 96, G: 104, B: 116, A: 255})
	small := r.face(10)
	if small == nil {
		return
	}
	hint := "No pets installed"
	hintX := rect.Min.X + (rect.Dx()-textWidth(small, hint))/2
	drawText(r.buf, small, hintX, titleY+lineHeight(small)+r.s(4), hint, color.NRGBA{R: 128, G: 136, B: 148, A: 255})
}

func (r *Renderer) drawBubble(text string, width int, approvalActions bool) (image.Rectangle, image.Rectangle, image.Rectangle) {
	face := r.face(12)
	if face == nil {
		return image.Rectangle{}, image.Rectangle{}, image.Rectangle{}
	}
	padX, padY := r.s(10), r.s(7)
	maxTextWidth := r.s(LogicalWidth-16) - 2*padX
	lines := wrapText(face, text, maxTextWidth, 3)
	if len(lines) == 0 {
		return image.Rectangle{}, image.Rectangle{}, image.Rectangle{}
	}
	widest := 0
	for _, line := range lines {
		if w := textWidth(face, line); w > widest {
			widest = w
		}
	}
	chipWidth := widest + 2*padX
	if approvalActions {
		minWidth := r.s(164)
		maxWidth := width - r.s(18)
		if maxWidth < minWidth {
			minWidth = maxWidth
		}
		if chipWidth < minWidth {
			chipWidth = minWidth
		}
		if chipWidth > maxWidth {
			chipWidth = maxWidth
		}
	}
	chipHeight := len(lines)*lineHeight(face) + 2*padY
	if approvalActions {
		chipHeight += r.s(34)
	}
	rect := image.Rect(0, 0, chipWidth, chipHeight).Add(image.Pt((width-chipWidth)/2, r.s(6)))
	fillRoundedRect(r.buf, rect, r.s(10), color.NRGBA{R: 255, G: 255, B: 255, A: 242})
	textColor := color.NRGBA{R: 32, G: 38, B: 46, A: 255}
	for index, line := range lines {
		drawText(r.buf, face, rect.Min.X+padX, rect.Min.Y+padY+index*lineHeight(face)+ascent(face), line, textColor)
	}
	if !approvalActions {
		return rect, image.Rectangle{}, image.Rectangle{}
	}

	gap := r.s(8)
	buttonHeight := r.s(22)
	innerWidth := max(0, rect.Dx()-r.s(22))
	buttonWidth := max(r.s(48), (innerWidth-gap)/2)
	y := rect.Max.Y - buttonHeight - r.s(6)
	approve := image.Rect(rect.Min.X+r.s(11), y, rect.Min.X+r.s(11)+buttonWidth, y+buttonHeight)
	deny := approve.Add(image.Pt(buttonWidth+gap, 0))
	r.drawApprovalButton("Approve", approve, color.NRGBA{R: 20, G: 122, B: 71, A: 255})
	r.drawApprovalButton("Deny", deny, color.NRGBA{R: 46, G: 46, B: 46, A: 255})
	return rect, approve, deny
}

func (r *Renderer) drawApprovalButton(text string, rect image.Rectangle, background color.NRGBA) {
	rect = rect.Intersect(r.buf.Bounds())
	if rect.Empty() {
		return
	}
	fillRoundedRect(r.buf, rect, r.s(6), background)
	face := r.face(10)
	if face == nil {
		return
	}
	x := rect.Min.X + (rect.Dx()-textWidth(face, text))/2
	y := rect.Min.Y + (rect.Dy()-lineHeight(face))/2 + ascent(face)
	drawText(r.buf, face, x, y, text, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
}

func (r *Renderer) drawChip(text string, logicalFontSize int, width int, bottom int, background color.NRGBA, textColor color.NRGBA) image.Rectangle {
	face := r.face(logicalFontSize)
	if face == nil {
		return image.Rectangle{}
	}
	padX, padY := r.s(10), r.s(5)
	chipWidth := textWidth(face, text) + 2*padX
	chipHeight := lineHeight(face) + 2*padY
	rect := image.Rect(0, 0, chipWidth, chipHeight).Add(image.Pt((width-chipWidth)/2, bottom-chipHeight))
	fillRoundedRect(r.buf, rect, chipHeight/2, background)
	drawText(r.buf, face, rect.Min.X+padX, rect.Min.Y+padY+ascent(face), text, textColor)
	return rect
}

func updatePill(state *protocol.UpdateState) (string, UpdateAction, color.NRGBA) {
	if state == nil {
		return "", UpdateActionNone, color.NRGBA{}
	}
	progress := color.NRGBA{R: 95, G: 103, B: 116, A: 235}
	switch state.Stage {
	case "checking":
		return "Checking for updates…", UpdateActionNone, progress
	case "fetching", "pulling":
		return "Updating…", UpdateActionNone, progress
	case "building":
		return "Building…", UpdateActionNone, progress
	case "restartPending":
		return "Restarting…", UpdateActionNone, color.NRGBA{R: 52, G: 168, B: 83, A: 235}
	case "failed":
		return "Update failed — click to dismiss", UpdateActionDismiss, color.NRGBA{R: 214, G: 69, B: 69, A: 235}
	}
	if !state.Available {
		return "", UpdateActionNone, color.NRGBA{}
	}
	accent := color.NRGBA{R: 64, G: 132, B: 244, A: 235}
	if state.CommitsBehind > 0 {
		return fmt.Sprintf("Update available (%d) — click", state.CommitsBehind), UpdateActionApply, accent
	}
	return "Rebuild available — click", UpdateActionApply, accent
}

func sessionText(count int) string {
	if count == 1 {
		return "1 active Pi session"
	}
	return fmt.Sprintf("%d active Pi sessions", count)
}

func clampRect(rect image.Rectangle, bounds image.Rectangle) image.Rectangle {
	return rect.Intersect(bounds)
}

func fillRoundedRect(dst *image.NRGBA, rect image.Rectangle, radius int, fill color.NRGBA) {
	rect = rect.Intersect(dst.Bounds())
	if rect.Empty() {
		return
	}
	maxRadius := rect.Dx() / 2
	if rect.Dy()/2 < maxRadius {
		maxRadius = rect.Dy() / 2
	}
	if radius > maxRadius {
		radius = maxRadius
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if !insideRounded(rect, radius, x, y) {
				continue
			}
			blendPixel(dst, x, y, fill)
		}
	}
}

func insideRounded(rect image.Rectangle, radius int, x int, y int) bool {
	if radius <= 0 {
		return true
	}
	cx, cy := x, y
	switch {
	case x < rect.Min.X+radius && y < rect.Min.Y+radius:
		cx, cy = rect.Min.X+radius, rect.Min.Y+radius
	case x >= rect.Max.X-radius && y < rect.Min.Y+radius:
		cx, cy = rect.Max.X-radius-1, rect.Min.Y+radius
	case x < rect.Min.X+radius && y >= rect.Max.Y-radius:
		cx, cy = rect.Min.X+radius, rect.Max.Y-radius-1
	case x >= rect.Max.X-radius && y >= rect.Max.Y-radius:
		cx, cy = rect.Max.X-radius-1, rect.Max.Y-radius-1
	default:
		return true
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}

// blendPixel applies non-premultiplied source-over compositing.
func blendPixel(dst *image.NRGBA, x int, y int, src color.NRGBA) {
	if src.A == 255 {
		dst.SetNRGBA(x, y, src)
		return
	}
	if src.A == 0 {
		return
	}
	offset := dst.PixOffset(x, y)
	dr := uint32(dst.Pix[offset])
	dg := uint32(dst.Pix[offset+1])
	db := uint32(dst.Pix[offset+2])
	da := uint32(dst.Pix[offset+3])
	sa := uint32(src.A)
	outA := sa*255 + da*(255-sa)
	if outA == 0 {
		dst.Pix[offset], dst.Pix[offset+1], dst.Pix[offset+2], dst.Pix[offset+3] = 0, 0, 0, 0
		return
	}
	blend := func(s uint8, d uint32) uint8 {
		return uint8((uint32(s)*sa*255 + d*da*(255-sa)) / outA)
	}
	dst.Pix[offset] = blend(src.R, dr)
	dst.Pix[offset+1] = blend(src.G, dg)
	dst.Pix[offset+2] = blend(src.B, db)
	dst.Pix[offset+3] = uint8(outA / 255)
}
