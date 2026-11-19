package notebook

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"image"
	"image/png"
	"io"
	"math"
)

const (
	maxPKDecompressed = 16 << 20
	maxPKStrokes      = 50_000
	maxPKPoints       = 500_000
)

type pkInk struct {
	r, g, b, a float64
}

type pkPoint struct {
	x, y    float64
	radius  float64
	opacity float64
}

type pkStroke struct {
	ink    pkInk
	points []pkPoint
}

// renderPKDrawing parses an Apple PencilKit PKDrawing blob (raw or gzip-wrapped
// "wrd\xf0" protobuf) and rasterizes all visible strokes into a transparent PNG.
// This enables handwriting annotations on platforms without Apple PencilKit
// (such as Android) as well as fallback rendering on desktop.
func renderPKDrawing(data []byte, width, height, pixels int) []byte {
	strokes, ok := parsePKDrawing(data)
	if !ok || len(strokes) == 0 {
		return nil
	}

	var rectX, rectY, rectW, rectH float64
	if width > 0 && height > 0 {
		rectW, rectH = float64(width), float64(height)
	} else {
		minX, minY := math.Inf(1), math.Inf(1)
		maxX, maxY := math.Inf(-1), math.Inf(-1)
		for _, s := range strokes {
			for _, p := range s.points {
				r := p.radius
				minX = min(minX, p.x-r)
				minY = min(minY, p.y-r)
				maxX = max(maxX, p.x+r)
				maxY = max(maxY, p.y+r)
			}
		}
		if math.IsInf(minX, 0) || math.IsNaN(minX) || maxX <= minX || maxY <= minY {
			return nil
		}
		rectX = math.Floor(minX - 4)
		rectY = math.Floor(minY - 4)
		rectW = math.Ceil(maxX+4) - rectX
		rectH = math.Ceil(maxY+4) - rectY
	}
	if rectW <= 0 || rectH <= 0 {
		return nil
	}

	scale := 2.0
	if pixels > 0 {
		scale = float64(pixels) / rectW
	}
	if math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 {
		return nil
	}
	outW := int(math.Round(rectW * scale))
	outH := int(math.Round(rectH * scale))
	if outW < 1 || outH < 1 || int64(outW)*int64(outH) > 28_000_000 {
		return nil
	}

	canvas := image.NewRGBA(image.Rect(0, 0, outW, outH))
	var covBuf []uint8

	for _, s := range strokes {
		if len(s.points) == 0 {
			continue
		}
		// Compute pixel bounding box of this stroke on the output canvas.
		minX, minY := outW, outH
		maxX, maxY := -1, -1
		for _, p := range s.points {
			px := (p.x - rectX) * scale
			py := (p.y - rectY) * scale
			pr := max(0.6, p.radius*scale) + 1.5
			x0 := max(0, int(math.Floor(px-pr)))
			y0 := max(0, int(math.Floor(py-pr)))
			x1 := min(outW-1, int(math.Ceil(px+pr)))
			y1 := min(outH-1, int(math.Ceil(py+pr)))
			if x0 <= x1 && y0 <= y1 {
				minX = min(minX, x0)
				minY = min(minY, y0)
				maxX = max(maxX, x1)
				maxY = max(maxY, y1)
			}
		}
		if minX > maxX || minY > maxY {
			continue
		}
		bw := maxX - minX + 1
		bh := maxY - minY + 1
		need := bw * bh
		if cap(covBuf) < need {
			covBuf = make([]uint8, need)
		} else {
			covBuf = covBuf[:need]
			clear(covBuf)
		}

		// Rasterize stroke capsules into per-stroke coverage buffer using max
		// blending so translucent markers do not darken where segments meet.
		n := len(s.points)
		if n == 1 {
			p := s.points[0]
			rasterCapsule(covBuf, bw, bh, minX, minY,
				(p.x-rectX)*scale, (p.y-rectY)*scale, max(0.6, p.radius*scale), p.opacity,
				(p.x-rectX)*scale, (p.y-rectY)*scale, max(0.6, p.radius*scale), p.opacity)
		} else {
			for i := 0; i < n-1; i++ {
				p0, p1 := s.points[i], s.points[i+1]
				rasterCapsule(covBuf, bw, bh, minX, minY,
					(p0.x-rectX)*scale, (p0.y-rectY)*scale, max(0.6, p0.radius*scale), p0.opacity,
					(p1.x-rectX)*scale, (p1.y-rectY)*scale, max(0.6, p1.radius*scale), p1.opacity)
			}
		}

		// Porter-Duff Over composite onto premultiplied RGBA canvas.
		sr := clamp01(s.ink.r)
		sg := clamp01(s.ink.g)
		sb := clamp01(s.ink.b)
		for y := 0; y < bh; y++ {
			rowOff := y * bw
			pixOff := (minY+y)*canvas.Stride + minX*4
			for x := 0; x < bw; x++ {
				cov := covBuf[rowOff+x]
				if cov == 0 {
					continue
				}
				sa := float64(cov) / 255.0
				inv := 1.0 - sa
				i := pixOff + x*4
				dr := float64(canvas.Pix[i+0]) / 255.0
				dg := float64(canvas.Pix[i+1]) / 255.0
				db := float64(canvas.Pix[i+2]) / 255.0
				da := float64(canvas.Pix[i+3]) / 255.0
				canvas.Pix[i+0] = toByte(sr*sa + dr*inv)
				canvas.Pix[i+1] = toByte(sg*sa + dg*inv)
				canvas.Pix[i+2] = toByte(sb*sa + db*inv)
				canvas.Pix[i+3] = toByte(sa + da*inv)
			}
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil
	}
	return out.Bytes()
}

func rasterCapsule(buf []uint8, bw, bh, offX, offY int, x0, y0, r0, op0, x1, y1, r1, op1 float64) {
	maxR := max(r0, r1) + 1.2
	bx0 := max(0, int(math.Floor(min(x0, x1)-maxR))-offX)
	by0 := max(0, int(math.Floor(min(y0, y1)-maxR))-offY)
	bx1 := min(bw-1, int(math.Ceil(max(x0, x1)+maxR))-offX)
	by1 := min(bh-1, int(math.Ceil(max(y0, y1)+maxR))-offY)
	if bx0 > bx1 || by0 > by1 {
		return
	}

	dx, dy := x1-x0, y1-y0
	lenSq := dx*dx + dy*dy
	for y := by0; y <= by1; y++ {
		py := float64(offY+y) + 0.5
		row := y * bw
		for x := bx0; x <= bx1; x++ {
			px := float64(offX+x) + 0.5
			var t float64
			if lenSq > 1e-9 {
				t = ((px-x0)*dx + (py-y0)*dy) / lenSq
				if t < 0 {
					t = 0
				} else if t > 1 {
					t = 1
				}
			}
			cx := x0 + dx*t
			cy := y0 + dy*t
			dist := math.Hypot(px-cx, py-cy)
			r := r0 + (r1-r0)*t
			aa := r + 0.5 - dist
			if aa <= 0 {
				continue
			}
			if aa > 1 {
				aa = 1
			}
			op := op0 + (op1-op0)*t
			cov := toByte(aa * op)
			if cov > buf[row+x] {
				buf[row+x] = cov
			}
		}
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func toByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255.0 + 0.5)
}

func parsePKDrawing(data []byte) ([]pkStroke, bool) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, false
		}
		raw, err := io.ReadAll(io.LimitReader(zr, maxPKDecompressed+1))
		_ = zr.Close()
		if err != nil || len(raw) > maxPKDecompressed {
			return nil, false
		}
		data = raw
	}
	if len(data) < 6 || !bytes.Equal(data[:4], []byte{'w', 'r', 'd', 0xf0}) {
		return nil, false
	}

	var inks []pkInk
	var rawStrokes [][]byte
	ok := walkProto(data[6:], func(field uint64, wire uint8, val uint64, raw []byte) bool {
		if wire != 2 {
			return true
		}
		switch field {
		case 4:
			inks = append(inks, parsePKInk(raw))
		case 5:
			if len(rawStrokes) < maxPKStrokes {
				rawStrokes = append(rawStrokes, raw)
			}
		}
		return true
	})
	if !ok {
		return nil, false
	}

	totalPoints := 0
	strokes := make([]pkStroke, 0, len(rawStrokes))
	for _, raw := range rawStrokes {
		stroke, pts, ok := parsePKStroke(raw, inks, maxPKPoints-totalPoints)
		if !ok || pts == 0 {
			continue
		}
		totalPoints += pts
		strokes = append(strokes, stroke)
		if totalPoints >= maxPKPoints {
			break
		}
	}
	return strokes, true
}

func parsePKInk(data []byte) pkInk {
	ink := pkInk{a: 1}
	walkProto(data, func(field uint64, wire uint8, _ uint64, raw []byte) bool {
		if wire == 2 && field == 1 {
			ink.r, ink.g, ink.b, ink.a = parsePKColor(raw)
		}
		return true
	})
	return ink
}

func parsePKColor(data []byte) (r, g, b, a float64) {
	a = 1
	walkProto(data, func(field uint64, wire uint8, val uint64, _ []byte) bool {
		if wire != 5 {
			return true
		}
		v := float64(math.Float32frombits(uint32(val)))
		switch field {
		case 1:
			r = v
		case 2:
			g = v
		case 3:
			b = v
		case 4:
			a = v
		}
		return true
	})
	return r, g, b, a
}

type pkTransform struct {
	a, b, c, d, tx, ty float64
}

func parsePKTransform(data []byte) pkTransform {
	tf := pkTransform{a: 1, d: 1}
	walkProto(data, func(field uint64, wire uint8, val uint64, _ []byte) bool {
		if wire != 5 {
			return true
		}
		v := float64(math.Float32frombits(uint32(val)))
		switch field {
		case 1:
			tf.a = v
		case 2:
			tf.b = v
		case 3:
			tf.c = v
		case 4:
			tf.d = v
		case 5:
			tf.tx = v
		case 6:
			tf.ty = v
		}
		return true
	})
	return tf
}

func parsePKStroke(data []byte, inks []pkInk, budget int) (pkStroke, int, bool) {
	var inkIndex uint64
	var flags uint64
	var pathRaw []byte
	tf := pkTransform{a: 1, d: 1}

	ok := walkProto(data, func(field uint64, wire uint8, val uint64, raw []byte) bool {
		switch {
		case field == 4 && wire == 0:
			inkIndex = val
		case field == 5 && wire == 2:
			pathRaw = raw
		case field == 7 && wire == 2:
			tf = parsePKTransform(raw)
		case field == 8 && wire == 0:
			flags = val
		}
		return true
	})
	// Bit 0 of flags marks tombstoned/erased strokes in PencilKit's CRDT.
	if !ok || len(pathRaw) == 0 || (flags&1) != 0 {
		return pkStroke{}, 0, false
	}

	ink := pkInk{a: 1}
	if int(inkIndex) < len(inks) {
		ink = inks[inkIndex]
	}

	pts := parsePKPath(pathRaw, tf, ink, budget)
	if len(pts) == 0 {
		return pkStroke{}, 0, false
	}
	return pkStroke{ink: ink, points: pts}, len(pts), true
}

// pkAttrSizes gives the byte size of each compressed PKStrokePoint bit (0..10).
var pkAttrSizes = [11]int{
	0:  8, // location (x, y float32)
	1:  4, // timeOffset (float32)
	2:  4, // radius / diameter (float32)
	3:  2, // aspectRatio (uint16 / 1000)
	4:  2, // edgeWidth (uint16)
	5:  2, // force (uint16 / 1000)
	6:  2, // azimuth (uint16)
	7:  2, // altitude (uint16)
	8:  2, // opacity (uint16 / 32767)
	9:  4, // secondaryScale / radius2 (float32)
	10: 2, // threshold (uint16)
}

func maskStride(mask uint64) (int, bool) {
	if mask >= 1<<len(pkAttrSizes) {
		return 0, false
	}
	total := 0
	for bit, size := range pkAttrSizes {
		if mask&(1<<bit) != 0 {
			total += size
		}
	}
	return total, true
}

func readMaskAttrs(buf []byte, mask uint64, x, y, size, opacity *float64) {
	off := 0
	for bit, sz := range pkAttrSizes {
		if mask&(1<<bit) == 0 {
			continue
		}
		switch bit {
		case 0:
			*x = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off : off+4])))
			*y = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off+4 : off+8])))
		case 2:
			*size = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off : off+4])))
		case 8:
			*opacity = float64(binary.LittleEndian.Uint16(buf[off:off+2])) / 32767.0
		}
		off += sz
	}
}

func parsePKPath(data []byte, tf pkTransform, ink pkInk, budget int) []pkPoint {
	var numPoints, pointMask, constMask uint64
	var constData, pointsData []byte

	if !walkProto(data, func(field uint64, wire uint8, val uint64, raw []byte) bool {
		switch {
		case field == 3 && wire == 0:
			numPoints = val
		case field == 4 && wire == 0:
			pointMask = val
		case field == 5 && wire == 0:
			constMask = val
		case field == 6 && wire == 2:
			constData = raw
		case field == 7 && wire == 2:
			pointsData = raw
		}
		return true
	}) {
		return nil
	}

	if numPoints == 0 || int(numPoints) > budget {
		return nil
	}
	constStride, ok := maskStride(constMask)
	if !ok || len(constData) < constStride {
		return nil
	}
	pointStride, ok := maskStride(pointMask)
	if !ok || pointStride == 0 || uint64(len(pointsData)) < numPoints*uint64(pointStride) {
		return nil
	}

	baseX, baseY := 0.0, 0.0
	baseSize := 4.0
	baseOpacity := 1.0
	readMaskAttrs(constData, constMask, &baseX, &baseY, &baseSize, &baseOpacity)

	tfScale := math.Sqrt(math.Abs(tf.a*tf.d - tf.b*tf.c))
	if math.IsNaN(tfScale) || math.IsInf(tfScale, 0) || tfScale <= 0 {
		tfScale = 1
	}

	pts := make([]pkPoint, 0, numPoints)
	for i := range numPoints {
		x, y, sz, op := baseX, baseY, baseSize, baseOpacity
		rec := pointsData[i*uint64(pointStride) : (i+1)*uint64(pointStride)]
		readMaskAttrs(rec, pointMask, &x, &y, &sz, &op)
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			continue
		}
		cx := tf.a*x + tf.c*y + tf.tx
		cy := tf.b*x + tf.d*y + tf.ty
		if sz <= 0 || math.IsNaN(sz) || math.IsInf(sz, 0) {
			sz = 4.0
		}
		if op <= 0 || math.IsNaN(op) {
			op = ink.a
		} else if ink.a < 0.99 && op >= 0.99 {
			op = ink.a
		}
		pts = append(pts, pkPoint{
			x:       cx,
			y:       cy,
			radius:  0.5 * sz * tfScale,
			opacity: clamp01(op),
		})
	}
	return pts
}

// walkProto decodes raw protobuf wire messages without schema codegen.
func walkProto(buf []byte, visit func(field uint64, wire uint8, val uint64, raw []byte) bool) bool {
	i := 0
	for i < len(buf) {
		tag, n := binary.Uvarint(buf[i:])
		if n <= 0 {
			return false
		}
		i += n
		field := tag >> 3
		wire := uint8(tag & 7)
		switch wire {
		case 0:
			v, m := binary.Uvarint(buf[i:])
			if m <= 0 {
				return false
			}
			i += m
			if !visit(field, wire, v, nil) {
				return true
			}
		case 1:
			if i+8 > len(buf) {
				return false
			}
			v := binary.LittleEndian.Uint64(buf[i : i+8])
			i += 8
			if !visit(field, wire, v, nil) {
				return true
			}
		case 2:
			ln, m := binary.Uvarint(buf[i:])
			if m <= 0 || ln > uint64(len(buf)-i-m) {
				return false
			}
			i += m
			raw := buf[i : i+int(ln)]
			i += int(ln)
			if !visit(field, wire, ln, raw) {
				return true
			}
		case 5:
			if i+4 > len(buf) {
				return false
			}
			v := uint64(binary.LittleEndian.Uint32(buf[i : i+4]))
			i += 4
			if !visit(field, wire, v, nil) {
				return true
			}
		default:
			return false
		}
	}
	return true
}
