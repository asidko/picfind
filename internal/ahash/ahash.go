package ahash

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math/bits"
	"strings"

	_ "golang.org/x/image/bmp"
)

const (
	size      = 8
	maxPixels = 64_000_000
	maxSide   = 32_768
)

type Hash uint64

var errFormat = errors.New("hash must be 16 hex chars, or 32 hex chars whose first 16 are the aHash")

func (h Hash) String() string {
	return fmt.Sprintf("%016x", uint64(h))
}

func Distance(a, b Hash) int {
	return bits.OnesCount64(uint64(a ^ b))
}

func Parse(s string) (Hash, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || (len(b) != 8 && len(b) != 16) {
		return 0, errFormat
	}
	return Hash(binary.BigEndian.Uint64(b)), nil
}

func FromReader(r io.ReadSeeker) (Hash, error) {
	img, format, err := decode(r)
	if err != nil {
		return 0, err
	}
	return fromImage(img, format), nil
}

func decode(r io.ReadSeeker) (image.Image, string, error) {
	cfg, format, err := image.DecodeConfig(r)
	if err != nil {
		return nil, "", err
	}
	if cfg.Width*cfg.Height > maxPixels || cfg.Width > maxSide || cfg.Height > maxSide {
		return nil, "", fmt.Errorf("image too large: %dx%d", cfg.Width, cfg.Height)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	if format != "gif" && format != "webp" {
		return image.Decode(r)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	if format == "gif" {
		keepTransparentColor(data)
	} else if animatedWebP(data) {
		img, err := firstWebPFrame(data)
		return img, format, err
	}
	return image.Decode(bytes.NewReader(data))
}

func fromImage(img image.Image, format string) Hash {
	px := thumbnail(img, format)
	sum := 0
	for _, p := range px {
		sum += int(p)
	}
	var h Hash
	for _, p := range px {
		h <<= 1
		if int(p)*len(px) > sum {
			h |= 1
		}
	}
	return h
}

func thumbnail(img image.Image, format string) []uint8 {
	b := img.Bounds()
	return resample(b.Dx(), b.Dy(), grayRows(img, format))
}

func luma(r, g, b uint8) uint8 {
	return uint8((uint32(r)*19595 + uint32(g)*38470 + uint32(b)*7471 + 0x8000) >> 16)
}

func grayOf(c color.Color) uint8 {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	return luma(n.R, n.G, n.B)
}

func premultipliedLuma(r, g, b, a uint8) uint8 {
	switch a16 := uint32(a) * 0x101; a16 {
	case 0xffff:
		return luma(r, g, b)
	case 0:
		return 0
	default:
		return luma(uint8(uint32(r)*0x101*0xffff/a16>>8), uint8(uint32(g)*0x101*0xffff/a16>>8), uint8(uint32(b)*0x101*0xffff/a16>>8))
	}
}

func grayRows(img image.Image, format string) rowFunc {
	b := img.Bounds()
	switch src := img.(type) {
	case *framed:
		fb := src.Image.Bounds()
		rows := grayRows(src.Image, format)
		return func(y int, dst []uint8) {
			clear(dst)
			if fy := y - src.at.Y; fy >= 0 && fy < fb.Dy() {
				rows(fy, dst[src.at.X:src.at.X+fb.Dx()])
			}
		}
	case *image.Gray:
		return func(y int, dst []uint8) {
			copy(dst, src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):])
		}
	case *image.Gray16:
		return func(y int, dst []uint8) {
			row := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):][:len(dst)*2]
			for x := range dst {
				if row[2*x] == 0 {
					dst[x] = row[2*x+1]
				} else {
					dst[x] = 255
				}
			}
		}
	case *image.YCbCr:
		return ycbcrRows(src, format)
	case *image.NYCbCrA:
		return ycbcrRows(&src.YCbCr, format)
	case *image.NRGBA:
		return func(y int, dst []uint8) {
			row := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):][:len(dst)*4]
			for x := range dst {
				p := row[x*4 : x*4+3]
				dst[x] = luma(p[0], p[1], p[2])
			}
		}
	case *image.NRGBA64:
		return func(y int, dst []uint8) {
			row := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):][:len(dst)*8]
			for x := range dst {
				p := row[x*8 : x*8+5]
				dst[x] = luma(p[0], p[2], p[4])
			}
		}
	case *image.RGBA:
		return func(y int, dst []uint8) {
			row := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):][:len(dst)*4]
			for x := range dst {
				p := row[x*4 : x*4+4]
				dst[x] = premultipliedLuma(p[0], p[1], p[2], p[3])
			}
		}
	case *image.Paletted:
		var lut [256]uint8
		for i, c := range src.Palette[:min(len(src.Palette), len(lut))] {
			lut[i] = grayOf(c)
		}
		return func(y int, dst []uint8) {
			row := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := range dst {
				dst[x] = lut[row[x]]
			}
		}
	}
	return func(y int, dst []uint8) {
		for x := range dst {
			dst[x] = grayOf(img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
}
