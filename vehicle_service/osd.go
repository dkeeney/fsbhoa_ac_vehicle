package main

import (
	"image"
	"image/jpeg"
	"math/bits"
	"os"
	"strings"
)

// The LPR cameras' description of a vehicle they read no plate for (CLAUDE.md, "Plate reads:
// FTP from the cameras"). For such a read the camera prints its guess at the vehicle on the
// scene photo, two lines over the bottom left:
//
//	Vehicle Color:grey  Vehicle Year:2017  Vehicle Type:suv
//	Vehicle Brand:Lincoln  Vehicle Model:Lincoln_MKC
//
// and uploads nothing else of it (the hour's summary file has only the plate, "Unknown"; no
// camera setting sends more). The text is a fixed pixel font on an 18-pixel grid from the left
// edge, the same at 2592 x 1520 (.137, .139, .140) and 1920 x 1080 (.138), white with a black
// outline, so each cell is matched with the letters' shapes (osd_font.go, cut from the photos
// of 2026-10-18 to 25). On those photos (83 reads without a plate) both lines read whole in
// 82; Tesseract misread the font ("201?", "Teela"). In infrared at night the camera's guess is
// worthless (a black Toyota sedan was a "black 2016 school bus"), so colorless photos are
// skipped.
const (
	osdCellW    = 18
	osdCellH    = 28
	osdMaxCells = 1600 / osdCellW // the lines are within the left 1600 pixels
	osdFrom     = 0.78            // the text is in the bottom 22% of the photo
	osdWhite    = 230             // a text pixel is at least this bright
	osdOutline  = 40              // with a pixel this dark within 2 pixels (the font's outline)
	osdMinInk   = 20              // fewer text pixels in a cell: a space
	osdMaxDiff  = 40              // a cell matches a letter differing in at most this many pixels
)

type osdGlyph struct {
	ch   rune
	rows [osdCellH]uint32
}

// osdTextMask marks the text pixels of the bottom of a frame, as rows of bits (bit 17 of a
// cell's 18 is its left column).
func osdTextMask(img image.Image) (mask [][]bool, ok bool) {
	ycc, isYCC := img.(*image.YCbCr)
	if !isYCC {
		return nil, false
	}
	b := ycc.Bounds()
	w := min(b.Dx(), osdMaxCells*osdCellW)
	y0 := b.Min.Y + int(float64(b.Dy())*osdFrom)
	h := b.Max.Y - y0
	lum := func(x, y int) int {
		if x < 0 || y < 0 || x >= w || y >= h {
			return 255
		}
		return int(ycc.Y[ycc.YOffset(b.Min.X+x, y0+y)])
	}
	mask = make([][]bool, h)
	for y := 0; y < h; y++ {
		mask[y] = make([]bool, w)
		for x := 0; x < w; x++ {
			if lum(x, y) < osdWhite {
				continue
			}
		outline:
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					if lum(x+dx, y+dy) <= osdOutline {
						mask[y][x] = true
						break outline
					}
				}
			}
		}
	}
	return mask, true
}

// osdLine reads `cells` cells of the text line whose cells start at row top.
func osdLine(mask [][]bool, top, cells int) string {
	var sb strings.Builder
	var cell [osdCellH]uint32
	for k := 0; k < cells; k++ {
		x0, ink := k*osdCellW, 0
		if top+osdCellH > len(mask) || x0+osdCellW > len(mask[0]) {
			break
		}
		for r := 0; r < osdCellH; r++ {
			var v uint32
			for c := 0; c < osdCellW; c++ {
				if mask[top+r][x0+c] {
					v |= 1 << (osdCellW - 1 - c)
				}
			}
			cell[r] = v
			ink += bits.OnesCount32(v)
		}
		if ink < osdMinInk {
			sb.WriteByte(' ')
			continue
		}
		best, bestDiff := ' ', 1<<30
		for _, g := range osdGlyphs {
			diff := 0
			for r := range cell {
				diff += bits.OnesCount32(cell[r] ^ g.rows[r])
			}
			if diff < bestDiff {
				best, bestDiff = g.ch, diff
			}
		}
		if bestDiff <= max(osdMaxDiff, ink*3/10) {
			sb.WriteRune(best)
		} else {
			sb.WriteByte(' ')
		}
	}
	return strings.TrimRight(sb.String(), " ")
}

// osdText reads the camera's "Vehicle ..." lines from a scene photo: each line is found where
// its first seven cells read "Vehicle". Nil for a colorless (infrared) photo.
func osdText(img image.Image) []string {
	if colorless(img) {
		return nil
	}
	mask, ok := osdTextMask(img)
	if !ok {
		return nil
	}
	var lines []string
	for top := 0; top+osdCellH <= len(mask); top++ {
		if osdLine(mask, top, 7) == "Vehicle" {
			lines = append(lines, osdLine(mask, top, osdMaxCells))
			top += osdCellH - 1
		}
	}
	return lines
}

// osdFields picks the "<Label>:<value>" fields from the lines (labels lower case: color,
// year, type, brand, model). Fields are two spaces apart, so a value may have single spaces
// ("pick up truck").
func osdFields(lines []string) map[string]string {
	fields := map[string]string{}
	for _, line := range lines {
		for _, part := range strings.Split(line, "  ") {
			label, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "Vehicle ")), ":")
			value = strings.TrimSpace(value)
			if ok && value != "" && value != "Unknown" {
				fields[strings.ToLower(label)] = value
			}
		}
	}
	return fields
}

// osdDescribe is the card's line for a vehicle without a plate, such as "grey 2017 Lincoln MKC
// (suv)": the color, the year, the model (which starts with the make) or else the make, and
// the type, leaving out what the camera didn't know. "" when it knew nothing.
func osdDescribe(fields map[string]string) string {
	var parts []string
	for _, k := range []string{"color", "year"} {
		if v := fields[k]; v != "" {
			parts = append(parts, v)
		}
	}
	if m := fields["model"]; m != "" {
		parts = append(parts, strings.ReplaceAll(m, "_", " "))
	} else if b := fields["brand"]; b != "" {
		parts = append(parts, b)
	}
	if t := fields["type"]; t != "" {
		if len(parts) == 0 {
			return t
		}
		parts = append(parts, "("+t+")")
	}
	return strings.Join(parts, " ")
}

// sceneDescription reads the camera's description from a scene photo file: the card's line and
// the lines as read. Empty when the file can't be read, it is infrared, or it has none.
func sceneDescription(path string) (desc string, lines []string) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return "", nil
	}
	lines = osdText(img)
	return osdDescribe(osdFields(lines)), lines
}
