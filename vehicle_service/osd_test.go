package main

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// osdFrame draws lines of text with the cameras' font, white with a black outline, over the
// bottom left of a w x h grey picture with some color, as the cameras print it.
func osdFrame(w, h int, lines []string, gap int) image.Image {
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio444)
	for i := range img.Y {
		img.Y[i], img.Cb[i], img.Cr[i] = 120, 110, 140
	}
	shape := map[rune][osdCellH]uint32{}
	for _, g := range osdGlyphs {
		if _, ok := shape[g.ch]; !ok {
			shape[g.ch] = g.rows
		}
	}
	top := int(float64(h)*osdFrom) + 20
	for li, line := range lines {
		for k, ch := range line {
			rows, ok := shape[ch]
			if !ok {
				continue
			}
			for r := 0; r < osdCellH; r++ {
				for c := 0; c < osdCellW; c++ {
					if rows[r]&(1<<(osdCellW-1-c)) == 0 {
						continue
					}
					x, y := k*osdCellW+c, top+li*gap+r
					for dy := -1; dy <= 1; dy++ { // the outline first, then the white
						for dx := -1; dx <= 1; dx++ {
							if o := img.YOffset(x+dx, y+dy); img.Y[o] != 245 {
								img.Y[o] = 0
							}
						}
					}
					img.Y[img.YOffset(x, y)] = 245
				}
			}
		}
	}
	return img
}

func TestOSDText(t *testing.T) {
	lines := []string{"Vehicle Color:grey  Vehicle Year:2017  Vehicle Type:suv", "Vehicle Brand:Lincoln  Vehicle Model:Lincoln_MKC"}
	for _, c := range []struct{ w, h, gap int }{{2592, 1520, 80}, {1920, 1080, 56}} {
		got := osdText(osdFrame(c.w, c.h, lines, c.gap))
		if fmt.Sprint(got) != fmt.Sprint(lines) {
			t.Errorf("%dx%d: read %q, want %q", c.w, c.h, got, lines)
		}
		if d := osdDescribe(osdFields(got)); d != "grey 2017 Lincoln MKC (suv)" {
			t.Errorf("%dx%d: description %q", c.w, c.h, d)
		}
	}
	// In infrared (no color) the camera's guess is worthless: nothing
	gray := image.NewGray(image.Rect(0, 0, 64, 64))
	gray.Set(1, 1, color.Gray{Y: 200})
	if got := osdText(gray); got != nil {
		t.Errorf("infrared frame read %q", got)
	}
}

func TestOSDDescribe(t *testing.T) {
	for _, c := range []struct {
		lines []string
		want  string
	}{
		{[]string{"Vehicle Color:white  Vehicle Year:2014  Vehicle Type:pick up truck", "Vehicle Brand:Unknown  Vehicle Model:Unknown"}, "white 2014 (pick up truck)"},
		{[]string{"Vehicle Color:black  Vehicle Year:Unknown  Vehicle Type:truck"}, "black (truck)"},
		{[]string{"Vehicle Color:black  Vehicle Year:2017  Vehicle Type:school bus", "Vehicle Brand:Ford  Vehicle Model:Unknown"}, "black 2017 Ford (school bus)"},
		{[]string{"Vehicle Color:Unknown  Vehicle Year:Unknown  Vehicle Type:Unknown", "Vehicle Brand:Unknown  Vehicle Model:Unknown"}, ""},
	} {
		if got := osdDescribe(osdFields(c.lines)); got != c.want {
			t.Errorf("%q: %q, want %q", c.lines, got, c.want)
		}
	}
}

// TestOSDReplay prints the description read from each scene photo listed in OSD_LIST (one path
// a line), or every *_src.jpg in OSD_DIR:
//
//	OSD_LIST=/path/list.txt go test -run OSDReplay -v
func TestOSDReplay(t *testing.T) {
	var files []string
	if list := os.Getenv("OSD_LIST"); list != "" {
		data, err := os.ReadFile(list)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range strings.Split(string(data), "\n") {
			if f = strings.TrimSpace(f); f != "" {
				files = append(files, f)
			}
		}
	} else if dir := os.Getenv("OSD_DIR"); dir != "" {
		files, _ = filepath.Glob(filepath.Join(dir, "*_src.jpg"))
	} else {
		t.Skip("OSD_LIST or OSD_DIR not set")
	}
	sort.Strings(files)
	for _, f := range files {
		desc, lines := sceneDescription(f)
		fmt.Printf("%s %q %q\n", filepath.Base(f), desc, lines)
	}
}
