package vtscreen

import (
	"image"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
)

// Selection tracks a cell-based drag selection within a visible pane.
type Selection struct {
	Start    image.Point
	End      image.Point
	Dragging bool
	Active   bool
}

func (s *Selection) Clear() { *s = Selection{} }

func (s *Selection) normalized() (a, b image.Point, ok bool) {
	if (!s.Active && !s.Dragging) || s.Start == s.End {
		return image.Point{}, image.Point{}, false
	}
	a, b = s.Start, s.End
	if a.Y > b.Y || (a.Y == b.Y && a.X > b.X) {
		a, b = b, a
	}
	return a, b, true
}

func (s *Selection) Contains(x, y int) bool {
	if s == nil {
		return false
	}
	a, b, ok := s.normalized()
	if !ok || y < a.Y || y > b.Y {
		return false
	}
	if a.Y == b.Y {
		return x >= a.X && x <= b.X
	}
	if y == a.Y {
		return x >= a.X
	}
	if y == b.Y {
		return x <= b.X
	}
	return true
}

// HandleMouse updates the selection from a host mouse event and reports whether
// the visual state changed or a completed drag is ready to copy.
func (s *Selection) HandleMouse(event uv.MouseEvent, area image.Rectangle) (changed bool, copyReady bool) {
	if area.Empty() {
		was := s.Active || s.Dragging
		s.Clear()
		return was, false
	}
	mouse := event.Mouse()
	point := image.Pt(mouse.X, mouse.Y)
	clamp := func(pt image.Point) image.Point {
		return image.Pt(
			min(area.Max.X-1, max(area.Min.X, pt.X))-area.Min.X,
			min(area.Max.Y-1, max(area.Min.Y, pt.Y))-area.Min.Y,
		)
	}
	switch event.(type) {
	case uv.MouseClickEvent:
		if mouse.Button != uv.MouseLeft {
			return false, false
		}
		if !point.In(area) {
			was := s.Active || s.Dragging
			s.Clear()
			return was, false
		}
		p := image.Pt(point.X-area.Min.X, point.Y-area.Min.Y)
		s.Start, s.End, s.Dragging, s.Active = p, p, true, false
		return true, false
	case uv.MouseMotionEvent:
		if !s.Dragging {
			return false, false
		}
		p := clamp(point)
		if p == s.End {
			return false, false
		}
		s.End = p
		s.Active = s.Start != s.End
		return true, false
	case uv.MouseReleaseEvent:
		if !s.Dragging || mouse.Button != uv.MouseLeft {
			return false, false
		}
		s.End = clamp(point)
		s.Dragging = false
		s.Active = s.Start != s.End
		return true, s.Active
	}
	return false, false
}

// Text extracts the selected cell contents, trimming trailing spaces per line.
func (s *Selection) Text(width, height int, at func(x, y int) *uv.Cell) string {
	if s == nil || width <= 0 || height <= 0 || at == nil {
		return ""
	}
	a, b, ok := s.normalized()
	if !ok {
		return ""
	}
	a.Y = min(height-1, max(0, a.Y))
	b.Y = min(height-1, max(0, b.Y))
	a.X = min(width-1, max(0, a.X))
	b.X = min(width-1, max(0, b.X))

	lines := make([]string, 0, b.Y-a.Y+1)
	for y := a.Y; y <= b.Y; y++ {
		x0, x1 := 0, width-1
		if y == a.Y {
			x0 = a.X
		}
		if y == b.Y {
			x1 = b.X
		}
		if x0 > 0 {
			if c := at(x0, y); c != nil && c.Width == 0 {
				x0--
			}
		}
		var row strings.Builder
		for x := x0; x <= x1; x++ {
			cell := at(x, y)
			if cell == nil {
				row.WriteByte(' ')
				continue
			}
			if cell.Width == 0 {
				continue
			}
			content := sanitizeCellContent(cell.Content)
			if content == "" {
				content = " "
			}
			row.WriteString(content)
		}
		lines = append(lines, strings.TrimRight(row.String(), " "))
	}
	return strings.Join(lines, "\n")
}

// RenderGrid renders a frozen cell grid with optional selection highlighting.
func RenderGrid(width int, grid []uv.Line, sel *Selection) []string {
	rows := make([]string, len(grid))
	for y, line := range grid {
		var selected func(int) bool
		if sel != nil && (sel.Active || sel.Dragging) {
			rowY := y
			selected = func(x int) bool { return sel.Contains(x, rowY) }
		}
		rows[y] = renderRowSelected(width, func(x int) *uv.Cell {
			if x < 0 || x >= len(line) {
				return nil
			}
			return &line[x]
		}, selected)
	}
	return rows
}
