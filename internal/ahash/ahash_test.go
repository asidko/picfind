package ahash

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func lcgGray(w, h, seed int) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	state := uint64(seed)
	for i := range g.Pix {
		state = (state*1103515245 + 12345) & 0x7fffffff
		g.Pix[i] = uint8(state >> 16)
	}
	return g
}

func gradientGray(w, h int) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			g.Pix[y*w+x] = uint8((x*7 + y*3) % 256)
		}
	}
	return g
}

func TestResizeMatchesPillow(t *testing.T) {
	raw, err := os.ReadFile("testdata/resize.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Pattern string  `json:"pattern"`
		W       int     `json:"w"`
		H       int     `json:"h"`
		Pixels  []uint8 `json:"pixels"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		src := gradientGray(c.W, c.H)
		if c.Pattern == "noise" {
			src = lcgGray(c.W, c.H, c.W*1000+c.H)
		}
		got := thumbnail(src, "png")
		if !slices.Equal(got, c.Pixels) {
			t.Errorf("%s %dx%d:\n got %v\nwant %v", c.Pattern, c.W, c.H, got, c.Pixels)
		}
	}
}

func TestHashMatchesImagehash(t *testing.T) {
	raw, err := os.ReadFile("testdata/hashes.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]struct {
		Hash   string  `json:"hash"`
		Pixels []uint8 `json:"pixels"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	for name, want := range golden {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := FromReader(f)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != want.Hash {
				t.Errorf("hash %s, want %s", got, want.Hash)
			}
			if _, err := f.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			img, format, err := decode(f)
			if err != nil {
				t.Fatal(err)
			}
			px := thumbnail(img, format)
			tolerance := 0
			if format == "jpeg" {
				tolerance = 1
			}
			for i := range px {
				if d := int(px[i]) - int(want.Pixels[i]); d > tolerance || d < -tolerance {
					t.Fatalf("8x8 pixels differ beyond %d:\n got %v\nwant %v", tolerance, px, want.Pixels)
				}
			}
		})
	}
}

func TestParse(t *testing.T) {
	ok := map[string]string{
		"3c7e7e3c18183c7e":                     "3c7e7e3c18183c7e",
		"3c7e7e3c18183c7e5a5aa5a50f0ff0f0":     "3c7e7e3c18183c7e",
		"  3C7E7E3C18183C7E5A5AA5A50F0FF0F0  ": "3c7e7e3c18183c7e",
		"0000000000000000":                     "0000000000000000",
	}
	for in, want := range ok {
		h, err := Parse(in)
		if err != nil || h.String() != want {
			t.Errorf("Parse(%q) = %v, %v; want %s", in, h, err, want)
		}
	}
	for _, in := range []string{"", "3c7e7e3c1818", "zz7e7e3c18183c7e", "0x7e7e3c18183c7e", "+c7e7e3c18183c7e", "3c7e7e3c18183c7e5a5aa5a50f0ff0f", "3c7e7e3c18183c7ezzzzzzzzzzzzzzzz"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) accepted invalid input", in)
		}
	}
}

func TestDistance(t *testing.T) {
	a, _ := Parse("3c7e7e3c18183c7e")
	b, _ := Parse("3c7e7e3c18183c71")
	if d := Distance(a, b); d != 4 {
		t.Errorf("distance %d, want 4", d)
	}
	if d := Distance(a, a); d != 0 {
		t.Errorf("self distance %d", d)
	}
}

func TestRejectsOversizedImages(t *testing.T) {
	for _, size := range []image.Point{{1, maxSide + 1}, {maxSide + 1, 1}} {
		var buf bytes.Buffer
		if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, size.X, size.Y))); err != nil {
			t.Fatal(err)
		}
		if _, err := FromReader(bytes.NewReader(buf.Bytes())); err == nil {
			t.Errorf("%dx%d accepted", size.X, size.Y)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, maxSide))); err != nil {
		t.Fatal(err)
	}
	if _, err := FromReader(bytes.NewReader(buf.Bytes())); err != nil {
		t.Errorf("1x%d rejected: %v", maxSide, err)
	}
}
