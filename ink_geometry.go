package main

import (
	"math"
	"slices"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo/ui"
)

type inkPoint = notebook.InkPoint
type inkStroke = notebook.Stroke

func distance(a, b inkPoint) float32 {
	return float32(math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y)))
}

func pointOn(a, b inkPoint, t float32) inkPoint {
	return inkPoint{X: a.X + t*(b.X-a.X), Y: a.Y + t*(b.Y-a.Y), Pressure: a.Pressure + t*(b.Pressure-a.Pressure)}
}

func distanceToSegment(p, a, b inkPoint) float32 {
	dx, dy := b.X-a.X, b.Y-a.Y
	if dx == 0 && dy == 0 {
		return distance(p, a)
	}
	t := clamp(((p.X-a.X)*dx+(p.Y-a.Y)*dy)/(dx*dx+dy*dy), 0, 1)
	return distance(p, pointOn(a, b, t))
}

func inkBounds(points []inkPoint) ui.Rect {
	if len(points) == 0 {
		return ui.Rect{}
	}
	left, top, right, bottom := points[0].X, points[0].Y, points[0].X, points[0].Y
	for _, point := range points[1:] {
		left, top = min(left, point.X), min(top, point.Y)
		right, bottom = max(right, point.X), max(bottom, point.Y)
	}
	return ui.Rect{X: left, Y: top, W: right - left, H: bottom - top}
}

type strokeBoundsCache struct {
	strokes  []inkStroke
	boxes    []ui.Rect
	extent   ui.Rect
	revision uint64
}

func (c *strokeBoundsCache) update(strokes []inkStroke) {
	if len(c.strokes) == len(strokes) && (len(strokes) == 0 || &c.strokes[0] == &strokes[0]) {
		return
	}
	c.boxes = slices.Grow(c.boxes, max(0, len(strokes)-len(c.boxes)))[:len(strokes)]
	c.extent = ui.Rect{}
	for i, stroke := range strokes {
		points := stroke.Points
		same := i < len(c.strokes) && len(c.strokes[i].Points) == len(points) &&
			(len(points) == 0 || &c.strokes[i].Points[0] == &points[0])
		if !same {
			c.boxes[i] = inkBounds(points)
		}
		if i == 0 {
			c.extent = c.boxes[i]
		} else {
			c.extent = unionRect(c.extent, c.boxes[i])
		}
	}
	c.strokes = strokes
	c.revision++
}

func unionRect(a, b ui.Rect) ui.Rect {
	x, y := min(a.X, b.X), min(a.Y, b.Y)
	return ui.Rect{X: x, Y: y, W: max(a.X+a.W, b.X+b.W) - x, H: max(a.Y+a.H, b.Y+b.H) - y}
}

func recognizeShape(points []inkPoint) []inkPoint {
	if len(points) < 4 {
		return nil
	}
	first, last := points[0], points[len(points)-1]
	bounds := inkBounds(points)
	size := max(bounds.W, bounds.H)
	endDistance := distance(first, last)
	if size < 24 {
		return nil
	}
	pressure := float32(0)
	var pathLen, maxDeviation float32
	for i, p := range points {
		pressure += p.Pressure
		maxDeviation = max(maxDeviation, distanceToSegment(p, first, last))
		if i != 0 {
			pathLen += distance(p, points[i-1])
		}
	}
	pressure /= float32(len(points))
	if endDistance >= size*.8 && pathLen < endDistance*1.23 && maxDeviation < size*.13 {
		first.Pressure, last.Pressure = pressure, pressure
		return []inkPoint{first, last}
	}

	if endDistance > size*.28 || min(bounds.W, bounds.H) < 18 || pathLen < (bounds.W+bounds.H)*1.15 {
		return nil
	}
	cx, cy := bounds.X+bounds.W/2, bounds.Y+bounds.H/2
	rx, ry := bounds.W/2, bounds.H/2
	var circleError, boxError float32
	for _, p := range points {
		x, y := (p.X-cx)/rx, (p.Y-cy)/ry
		circleError += abs(float32(math.Hypot(float64(x), float64(y))) - 1)
		boxError += abs(max(abs(x), abs(y)) - 1)
	}
	tolerance := float32(len(points)) * .24
	if min(circleError, boxError) > tolerance {
		return nil
	}
	if boxError < circleError*.92 {
		return []inkPoint{
			{X: bounds.X, Y: bounds.Y, Pressure: pressure}, {X: bounds.X + bounds.W, Y: bounds.Y, Pressure: pressure},
			{X: bounds.X + bounds.W, Y: bounds.Y + bounds.H, Pressure: pressure}, {X: bounds.X, Y: bounds.Y + bounds.H, Pressure: pressure},
			{X: bounds.X, Y: bounds.Y, Pressure: pressure},
		}
	}
	ellipse := make([]inkPoint, 65)
	for i := range ellipse {
		t := 2 * math.Pi * float64(i) / float64(len(ellipse)-1)
		ellipse[i] = inkPoint{X: cx + rx*float32(math.Cos(t)), Y: cy + ry*float32(math.Sin(t)), Pressure: pressure}
	}
	return ellipse
}

func abs(x float32) float32 { return max(x, -x) }

func inkPath(points []inkPoint, ox, oy, scale float32) *ui.Path {
	path := &ui.Path{}
	if len(points) > 0 {
		path.MoveTo(ox+points[0].X*scale, oy+points[0].Y*scale)
		for _, p := range points[1:] {
			path.LineTo(ox+p.X*scale, oy+p.Y*scale)
		}
	}
	return path
}

func strokeColor(stroke inkStroke, p palette) ui.Color {
	if stroke.Color == "" {
		return p.ink
	}
	if len(stroke.Color) != 7 || stroke.Color[0] != '#' {
		return p.ink
	}
	return ui.Hex(stroke.Color)
}

func paintStroke(paint *ui.Painter, stroke inkStroke, ox, oy, scale float32, color ui.Color) {
	paintBoundedStroke(paint, stroke, inkBounds(stroke.Points), ox, oy, scale, color)
}

func paintBoundedStroke(paint *ui.Painter, stroke inkStroke, b ui.Rect, ox, oy, scale float32, color ui.Color) {
	if len(stroke.Points) == 0 {
		return
	}
	margin := stroke.Width * scale
	vis := paint.Visible()
	if ox+(b.X+b.W)*scale+margin < vis.X || ox+b.X*scale-margin > vis.X+vis.W ||
		oy+(b.Y+b.H)*scale+margin < vis.Y || oy+b.Y*scale-margin > vis.Y+vis.H {
		return
	}
	radius := func(p inkPoint) float32 {
		if stroke.Marker {
			return stroke.Width * scale / 2
		}
		return stroke.Width * scale * (.13 + .37*float32(math.Sqrt(float64(clamp(p.Pressure, 0, 1)))))
	}
	if len(stroke.Points) == 1 {
		p := stroke.Points[0]
		r := radius(p)
		paint.Fill(ui.Rect{X: ox + p.X*scale - r, Y: oy + p.Y*scale - r, W: 2 * r, H: 2 * r}, color, r)
		return
	}
	if stroke.Marker {
		paint.StrokePath(inkPath(stroke.Points, ox, oy, scale), stroke.Width*scale, color)
		return
	}
	geometry := &ui.Path{}
	// Each tapered segment and join is a contour with the same winding.
	// Filling their union gives a continuous, pressure-dependent nib.
	for i, pt := range stroke.Points {
		x, y, r := ox+pt.X*scale, oy+pt.Y*scale, radius(pt)
		geometry.Circle(x, y, r)
		if i == 0 {
			continue
		}
		prev := stroke.Points[i-1]
		length := distance(prev, pt)
		if length <= .001 {
			continue
		}
		nx, ny := -(pt.Y-prev.Y)/length, (pt.X-prev.X)/length
		ax, ay, ar := ox+prev.X*scale, oy+prev.Y*scale, radius(prev)
		geometry.MoveTo(ax+nx*ar, ay+ny*ar).LineTo(ax-nx*ar, ay-ny*ar).
			LineTo(x-nx*r, y-ny*r).LineTo(x+nx*r, y+ny*r).Close()
	}
	paint.FillPath(geometry, color)
}

func pointInLasso(point inkPoint, polygon []inkPoint) bool {
	inside := false
	prev := polygon[len(polygon)-1]
	for _, cur := range polygon {
		if (cur.Y > point.Y) != (prev.Y > point.Y) &&
			point.X < (prev.X-cur.X)*(point.Y-cur.Y)/(prev.Y-cur.Y)+cur.X {
			inside = !inside
		}
		prev = cur
	}
	return inside
}

// sampleStroke provides intermediate points so erasing and lassoing also
// work on long straight segments created by the shape recognizer.
func sampleStroke(points []inkPoint, step float32, visit func(inkPoint) bool) bool {
	if len(points) == 0 || visit(points[0]) {
		return len(points) != 0
	}
	for i, end := range points[1:] {
		start := points[i]
		parts := min(8000, max(1, int(math.Ceil(float64(distance(start, end)/step)))))
		for j := 1; j <= parts; j++ {
			if visit(pointOn(start, end, float32(j)/float32(parts))) {
				return true
			}
		}
	}
	return false
}

func eraseStrokes(strokes []inkStroke, trail []inkPoint, radius float32, whole bool) ([]inkStroke, bool) {
	if len(trail) == 0 {
		return strokes, false
	}
	result := make([]inkStroke, 0, len(strokes))
	changed := false
	sweep := inkBounds(trail)
	for _, stroke := range strokes {
		r := radius + stroke.Width/2
		b := inkBounds(stroke.Points)
		if b.X+b.W < sweep.X-r || b.Y+b.H < sweep.Y-r || b.X > sweep.X+sweep.W+r || b.Y > sweep.Y+sweep.H+r {
			result = append(result, stroke)
			continue
		}
		hits := func(p inkPoint) bool {
			if distance(p, trail[0]) <= r {
				return true
			}
			for i, q := range trail[1:] {
				if distanceToSegment(p, trail[i], q) <= r {
					return true
				}
			}
			return false
		}
		step := max(.0001, radius/3)
		if !sampleStroke(stroke.Points, step, hits) {
			result = append(result, stroke)
			continue
		}
		changed = true
		if whole {
			continue
		}
		var fragment []inkPoint
		flush := func() {
			if len(fragment) > 0 {
				piece := stroke
				piece.Points = fragment
				result = append(result, piece)
				fragment = nil
			}
		}
		sampleStroke(stroke.Points, step, func(p inkPoint) bool {
			if hits(p) {
				flush()
			} else {
				fragment = append(fragment, p)
			}
			return false
		})
		flush()
	}
	return result, changed
}
