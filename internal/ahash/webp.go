package ahash

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"iter"

	"golang.org/x/image/webp"
)

var errAnimation = errors.New("webp: invalid animation")

type framed struct {
	image.Image
	canvas image.Rectangle
	at     image.Point
}

func (f *framed) Bounds() image.Rectangle {
	return f.canvas
}

func (f *framed) At(x, y int) color.Color {
	b := f.Image.Bounds()
	p := image.Pt(x, y).Sub(f.at).Add(b.Min)
	if !p.In(b) {
		return color.NRGBA{}
	}
	return f.Image.At(p.X, p.Y)
}

func uint24(b []byte) int {
	return int(b[0]) | int(b[1])<<8 | int(b[2])<<16
}

func riffChunks(data []byte) iter.Seq2[string, []byte] {
	return func(yield func(string, []byte) bool) {
		for len(data) >= 8 {
			id, n := string(data[:4]), int(binary.LittleEndian.Uint32(data[4:8]))
			data = data[8:]
			if n > len(data) || !yield(id, data[:n]) {
				return
			}
			data = data[min(n+n%2, len(data)):]
		}
	}
}

func animatedWebP(data []byte) bool {
	return len(data) >= 30 && string(data[12:16]) == "VP8X" && data[20]&0x02 != 0
}

func firstWebPFrame(data []byte) (image.Image, error) {
	canvas := image.Rect(0, 0, uint24(data[24:])+1, uint24(data[27:])+1)
	for id, anmf := range riffChunks(data[12:]) {
		if id != "ANMF" {
			continue
		}
		if len(anmf) < 16 {
			return nil, errAnimation
		}
		at := image.Pt(2*uint24(anmf[0:]), 2*uint24(anmf[3:]))
		frame := image.Rectangle{at, at.Add(image.Pt(uint24(anmf[6:])+1, uint24(anmf[9:])+1))}
		if !frame.In(canvas) {
			return nil, errAnimation
		}
		var flags byte
		for sub := range riffChunks(anmf[16:]) {
			if sub == "ALPH" {
				flags = 0x10
			}
		}
		vp8x := append([]byte{flags, 0, 0, 0}, anmf[6:12]...)
		file := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00"), vp8x...)
		file = append(file, anmf[16:]...)
		binary.LittleEndian.PutUint32(file[4:], uint32(len(file)-8))
		img, err := webp.Decode(bytes.NewReader(file))
		if err != nil {
			return nil, err
		}
		return &framed{img, canvas, at}, nil
	}
	return nil, errAnimation
}
