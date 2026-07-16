package ui

import (
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// ---------------------------------------------------------------------------
// Layouts
// ---------------------------------------------------------------------------

// columnsLayout lays children out as columns, the way a CSS grid distributes
// `minmax(min, Nfr)`. Fyne's GridWithColumns gives every column an equal share,
// which is wrong for a table where "#" needs a sliver and "Task" needs room to
// breathe.
//
// Every column is given its minimum first, and only the surplus is shared out
// by weight. Distributing purely by weight is not enough: a status pill reading
// "Pushed — Complete" or a "Complete" button is a fixed lump of pixels that
// cannot ellipsise, so a column narrower than its content does not shrink — it
// spills over the top of its neighbour.
//
// The minimums also flow up through MinSize into the window's own minimum, so
// the table can never be squeezed to the point where cells collide.
type columnsLayout struct {
	columns []column
	spacing float32
}

// column is one column's floor and its share of whatever is left over.
type column struct {
	min    float32
	weight float32
}

func (l columnsLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var height float32
	for _, o := range objs {
		if h := o.MinSize().Height; h > height {
			height = h
		}
	}

	var width float32
	for _, c := range l.columns {
		width += c.min
	}
	if n := len(l.columns); n > 1 {
		width += l.spacing * float32(n-1)
	}
	return fyne.NewSize(width, height)
}

func (l columnsLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 || len(objs) != len(l.columns) {
		return
	}

	var floor, weight float32
	for _, c := range l.columns {
		floor += c.min
		weight += c.weight
	}

	available := size.Width - l.spacing*float32(len(objs)-1)

	// Below the floor the columns simply take their minimums and the row
	// overflows; the window's MinSize is what stops that happening in practice.
	surplus := available - floor
	if surplus < 0 {
		surplus = 0
	}

	var x float32
	for i, o := range objs {
		w := l.columns[i].min
		if weight > 0 {
			w += surplus * l.columns[i].weight / weight
		}

		o.Move(fyne.NewPos(x, 0))
		o.Resize(fyne.NewSize(w, size.Height))
		x += w + l.spacing
	}
}

// insetLayout pads a single child by an explicit amount. Fyne's Padded
// container uses the theme's padding, which is tuned for widgets sitting next
// to each other, not for the generous interior of a card.
type insetLayout struct {
	top, right, bottom, left float32
}

func (l insetLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var m fyne.Size
	for _, o := range objs {
		m = m.Max(o.MinSize())
	}
	return fyne.NewSize(m.Width+l.left+l.right, m.Height+l.top+l.bottom)
}

func (l insetLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(l.left, l.top))
		o.Resize(fyne.NewSize(size.Width-l.left-l.right, size.Height-l.top-l.bottom))
	}
}

// sizedLayout pins a child to an explicit width, height, or both. A zero on
// either axis means "keep whatever the child asks for".
//
// This exists because the obvious tool — layout.NewGridWrapLayout — divides by
// its cell width to work out how many columns fit, so passing a zero on either
// axis yields a non-finite MinSize. That propagates all the way up to the
// window, which then never gets a valid size and simply never appears.
type sizedLayout struct{ w, h float32 }

func (l sizedLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var m fyne.Size
	for _, o := range objs {
		m = m.Max(o.MinSize())
	}
	if l.w > 0 {
		m.Width = l.w
	}
	if l.h > 0 {
		m.Height = l.h
	}
	return m
}

func (l sizedLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

// sized fixes o's width and/or height; pass 0 for an axis to leave it natural.
func sized(o fyne.CanvasObject, w, h float32) *fyne.Container {
	return container.New(sizedLayout{w: w, h: h}, o)
}

// inset wraps o with the same padding on every side.
func inset(o fyne.CanvasObject, all float32) *fyne.Container {
	return container.New(insetLayout{all, all, all, all}, o)
}

// insetXY wraps o with separate horizontal and vertical padding.
func insetXY(o fyne.CanvasObject, x, y float32) *fyne.Container {
	return container.New(insetLayout{y, x, y, x}, o)
}

// ---------------------------------------------------------------------------
// Primitives
// ---------------------------------------------------------------------------

// surface is a rounded, bordered rectangle: the base of every card and table.
func surface(fill color.Color, radius float32) *canvas.Rectangle {
	r := canvas.NewRectangle(fill)
	r.CornerRadius = radius
	r.StrokeColor = colBorder
	r.StrokeWidth = 1
	return r
}

// card is a titled panel. A blank title omits the heading entirely, which is
// what the table card wants.
func card(title string, content fyne.CanvasObject) fyne.CanvasObject {
	body := content
	if title != "" {
		body = container.NewBorder(
			container.New(insetLayout{bottom: 14}, sectionLabel(title)),
			nil, nil, nil, content)
	}
	return container.NewStack(surface(colCard, radiusCard), inset(body, 18))
}

// sectionLabel is the small, muted, upper-case heading above a card's content
// — "CURRENT TIMER", "TODAY'S SUMMARY". Fyne has no letter-spacing, so the
// upper-casing carries the whole effect on its own.
func sectionLabel(text string) *canvas.Text {
	t := canvas.NewText(strings.ToUpper(text), colTextMuted)
	t.TextSize = 11
	t.TextStyle.Bold = true
	return t
}

// muted is secondary body copy.
func muted(text string) *canvas.Text {
	t := canvas.NewText(text, colTextMuted)
	t.TextSize = 13
	return t
}

// heading is a page title.
func heading(text string) *canvas.Text {
	t := canvas.NewText(text, colText)
	t.TextSize = 21
	t.TextStyle.Bold = true
	return t
}

// hairline is a one-pixel rule in the border colour.
func hairline() *canvas.Rectangle {
	r := canvas.NewRectangle(colBorder)
	r.SetMinSize(fyne.NewSize(1, 1))
	return r
}

// hug keeps o at its minimum size, pinned left and vertically centred, instead
// of letting it stretch to fill its cell. Status pills and small buttons want
// to be the size of their content and no larger.
func hug(o fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVBox(
		layout.NewSpacer(),
		container.NewHBox(o, layout.NewSpacer()),
		layout.NewSpacer(),
	)
}

// centreY vertically centres o without constraining its width.
func centreY(o fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVBox(layout.NewSpacer(), o, layout.NewSpacer())
}

// ---------------------------------------------------------------------------
// Status pill
// ---------------------------------------------------------------------------

// pill renders a rounded status chip. Completed work is shown in the muted
// palette rather than the success one, so a finished task does not shout as
// loudly as freshly logged work.
func pill(text string, fg, fill color.Color) fyne.CanvasObject {
	bg := canvas.NewRectangle(fill)
	bg.CornerRadius = radiusPill

	label := canvas.NewText(text, fg)
	label.TextSize = 11
	label.TextStyle.Bold = true

	return container.NewStack(bg, insetXY(label, 10, 5))
}

// statusPill colours a session's status. Anything pushed upstream reads as
// success; work still in flight is neutral.
func statusPill(status string, done bool) fyne.CanvasObject {
	switch {
	case done:
		return pill(status, colTextMuted, rgba(0x8494A9, 0x24))
	case strings.HasPrefix(status, "Push"):
		return pill(status, colAccent, rgba(0x5B9CFF, 0x2E))
	case status == "In Progress":
		return pill(status, colAccent, rgba(0x5B9CFF, 0x2E))
	default:
		return pill(status, colPillText, colPillFill)
	}
}

// ---------------------------------------------------------------------------
// Avatar
// ---------------------------------------------------------------------------

// avatar is the round initials chip in the header.
func avatar(initials string, size float32) fyne.CanvasObject {
	circle := canvas.NewCircle(colAvatar)

	text := canvas.NewText(strings.ToUpper(initials), colText)
	text.TextSize = 12
	text.TextStyle.Bold = true
	text.Alignment = fyne.TextAlignCenter

	stack := container.NewStack(circle, container.NewCenter(text))
	return sized(stack, size, size)
}

// initials reduces a username to at most two letters for the avatar.
func initials(name string) string {
	fields := strings.FieldsFunc(name, func(r rune) bool {
		return r == ' ' || r == '.' || r == '_' || r == '-'
	})
	switch len(fields) {
	case 0:
		return "?"
	case 1:
		if len(fields[0]) >= 2 {
			return fields[0][:2]
		}
		return fields[0]
	default:
		return string(fields[0][0]) + string(fields[1][0])
	}
}

// dot is the small filled circle beside a status word, e.g. "Online".
func dot(c color.Color, size float32) fyne.CanvasObject {
	circle := canvas.NewCircle(c)
	return sized(circle, size, size)
}

// ---------------------------------------------------------------------------
// Nav item
// ---------------------------------------------------------------------------

// navItem is one row of the sidebar. It is a custom widget rather than a themed
// button because it needs three visual states — idle, hovered, selected — and a
// selected state that fills with the primary colour, which no stock Fyne button
// offers.
type navItem struct {
	widget.BaseWidget

	label   string
	iconOff fyne.Resource
	iconOn  fyne.Resource

	selected bool
	hovered  bool
	onTap    func()
}

const navItemHeight float32 = 44

func newNavItem(label string, mkIcon func(color.Color) fyne.Resource, onTap func()) *navItem {
	n := &navItem{
		label:   label,
		iconOff: mkIcon(colTextMuted),
		iconOn:  mkIcon(colText),
		onTap:   onTap,
	}
	n.ExtendBaseWidget(n)
	return n
}

func (n *navItem) SetSelected(v bool) {
	if n.selected == v {
		return
	}
	n.selected = v
	n.Refresh()
}

func (n *navItem) Tapped(*fyne.PointEvent) {
	if n.onTap != nil {
		n.onTap()
	}
}

func (n *navItem) MouseIn(*desktop.MouseEvent)    { n.hovered = true; n.Refresh() }
func (n *navItem) MouseOut()                      { n.hovered = false; n.Refresh() }
func (n *navItem) MouseMoved(*desktop.MouseEvent) {}

func (n *navItem) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (n *navItem) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(color.Transparent)
	bg.CornerRadius = radiusNav

	img := canvas.NewImageFromResource(n.iconOff)
	img.FillMode = canvas.ImageFillContain

	text := canvas.NewText(n.label, colTextMuted)
	text.TextSize = 13
	text.TextStyle.Bold = true

	r := &navItemRenderer{item: n, bg: bg, img: img, text: text}
	r.Refresh()
	return r
}

var (
	_ fyne.Widget        = (*navItem)(nil)
	_ fyne.Tappable      = (*navItem)(nil)
	_ desktop.Hoverable  = (*navItem)(nil)
	_ desktop.Cursorable = (*navItem)(nil)
)

type navItemRenderer struct {
	item *navItem
	bg   *canvas.Rectangle
	img  *canvas.Image
	text *canvas.Text
}

const (
	navIconSize float32 = 18
	navIconX    float32 = 14
	navTextX    float32 = 44
)

func (r *navItemRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.bg.Move(fyne.NewPos(0, 0))

	r.img.Resize(fyne.NewSize(navIconSize, navIconSize))
	r.img.Move(fyne.NewPos(navIconX, (size.Height-navIconSize)/2))

	textHeight := r.text.MinSize().Height
	r.text.Move(fyne.NewPos(navTextX, (size.Height-textHeight)/2))
}

func (r *navItemRenderer) MinSize() fyne.Size {
	return fyne.NewSize(navTextX+r.text.MinSize().Width+navIconX, navItemHeight)
}

func (r *navItemRenderer) Refresh() {
	switch {
	case r.item.selected:
		r.bg.FillColor = colPrimary
		r.img.Resource = r.item.iconOn
		r.text.Color = colText
	case r.item.hovered:
		r.bg.FillColor = colHover
		r.img.Resource = r.item.iconOn
		r.text.Color = colText
	default:
		r.bg.FillColor = color.Transparent
		r.img.Resource = r.item.iconOff
		r.text.Color = colTextMuted
	}

	r.bg.Refresh()
	r.img.Refresh()
	r.text.Refresh()
}

func (r *navItemRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.img, r.text}
}

func (r *navItemRenderer) Destroy() {}

// ---------------------------------------------------------------------------
// Inputs
// ---------------------------------------------------------------------------

// searchField overlays a magnifier on the trailing edge of an entry. Fyne's
// Entry cannot carry an adornment, so the icon is stacked on top of it; a
// canvas.Image is not tappable, so clicks still land on the entry underneath.
func searchField(entry *widget.Entry) fyne.CanvasObject {
	icon := canvas.NewImageFromResource(iconSearch(colTextDim))
	icon.FillMode = canvas.ImageFillContain
	icon.SetMinSize(fyne.NewSize(15, 15))

	trailing := container.NewHBox(layout.NewSpacer(), icon)
	return container.NewStack(entry, insetXY(centreY(trailing), 10, 0))
}

// iconButton is a Fyne button carrying one of the stroke icons. Importance
// decides the fill, so the icon is built white for the primary variant and
// muted for the secondary one.
func iconButton(label string, mkIcon func(color.Color) fyne.Resource, primary bool, tap func()) *widget.Button {
	tint := colTextMuted
	if primary {
		tint = colText
	}

	b := widget.NewButtonWithIcon(label, mkIcon(tint), tap)
	if primary {
		b.Importance = widget.HighImportance
	}
	return b
}

// ---------------------------------------------------------------------------
// Charts
// ---------------------------------------------------------------------------

// meter is a horizontal proportion bar: a track with a filled portion. It backs
// the per-task and per-source breakdowns on the Reports page.
func meter(fraction float64, fill color.Color) fyne.CanvasObject {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}

	track := canvas.NewRectangle(rgba(0xFFFFFF, 0x10))
	track.CornerRadius = 3

	bar := canvas.NewRectangle(fill)
	bar.CornerRadius = 3

	return container.New(&meterLayout{fraction: float32(fraction)}, track, bar)
}

type meterLayout struct{ fraction float32 }

func (l *meterLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(60, 6)
}

func (l *meterLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) != 2 {
		return
	}
	objs[0].Move(fyne.NewPos(0, 0))
	objs[0].Resize(size)

	objs[1].Move(fyne.NewPos(0, 0))
	objs[1].Resize(fyne.NewSize(size.Width*l.fraction, size.Height))
}

// Chart geometry, shared by the bars and the labels underneath so the two rows
// line up.
const (
	chartGap      float32 = 6
	chartMaxBar   float32 = 44  // a seven-day chart across a wide window would
	chartHeight   float32 = 130 // otherwise draw bars the width of a door
	chartLabelPad float32 = 6
)

// slotWidth is the horizontal pitch of one column, and barWidth the drawn width
// within it. Bars are centred in their slot once the slot exceeds the cap.
func slotWidth(total float32, n int) (slot, bar float32) {
	if n <= 0 {
		return 0, 0
	}
	slot = (total - chartGap*float32(n-1)) / float32(n)

	bar = slot
	if bar > chartMaxBar {
		bar = chartMaxBar
	}
	return slot, bar
}

// columnChart draws one column per bucket: a faint full-height track, and the
// filled bar on top of it. The track matters — without it an idle day is not a
// short bar, it is nothing at all, and the eye reads a gap in the axis as
// missing data rather than as a day off.
type columnChart struct {
	fractions []float64
}

func (c *columnChart) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(float32(len(c.fractions))*10, chartHeight)
}

func (c *columnChart) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	n := len(c.fractions)
	if n == 0 || len(objs) != 2*n {
		return
	}

	slot, bar := slotWidth(size.Width, n)

	for i := 0; i < n; i++ {
		x := float32(i)*(slot+chartGap) + (slot-bar)/2

		track := objs[i]
		track.Move(fyne.NewPos(x, 0))
		track.Resize(fyne.NewSize(bar, size.Height))

		// A day with any work at all draws at least a nub, so "barely worked"
		// and "did not work" stay visually distinct.
		h := size.Height * float32(c.fractions[i])
		if c.fractions[i] > 0 && h < 3 {
			h = 3
		}

		fill := objs[n+i]
		fill.Move(fyne.NewPos(x, size.Height-h))
		fill.Resize(fyne.NewSize(bar, h))
	}
}

// chartLabels centres one caption under each column, on the same pitch.
type chartLabels struct{ n int }

func (l *chartLabels) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var h float32
	for _, o := range objs {
		if m := o.MinSize().Height; m > h {
			h = m
		}
	}
	return fyne.NewSize(float32(l.n)*10, h)
}

func (l *chartLabels) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	slot, _ := slotWidth(size.Width, l.n)

	for i, o := range objs {
		o.Move(fyne.NewPos(float32(i)*(slot+chartGap), 0))
		o.Resize(fyne.NewSize(slot, size.Height))
	}
}

// newColumnChart builds the bars and, when labels are supplied, the caption row
// beneath them. Pass a nil labels slice to omit the captions, which is what a
// ninety-day chart wants — ninety captions is a smear, not an axis.
func newColumnChart(fractions []float64, labels []string, fill color.Color) fyne.CanvasObject {
	n := len(fractions)

	objs := make([]fyne.CanvasObject, 0, 2*n)
	for range fractions {
		track := canvas.NewRectangle(rgba(0xFFFFFF, 0x0A))
		track.CornerRadius = 3
		objs = append(objs, track)
	}
	for range fractions {
		bar := canvas.NewRectangle(fill)
		bar.CornerRadius = 3
		objs = append(objs, bar)
	}

	bars := sized(container.New(&columnChart{fractions: fractions}, objs...), 0, chartHeight)
	if len(labels) != n {
		return bars
	}

	captions := make([]fyne.CanvasObject, 0, n)
	for _, text := range labels {
		t := canvas.NewText(text, colTextDim)
		t.TextSize = 10
		t.Alignment = fyne.TextAlignCenter
		captions = append(captions, t)
	}

	return container.NewVBox(
		bars,
		container.New(insetLayout{top: chartLabelPad},
			container.New(&chartLabels{n: n}, captions...)),
	)
}
