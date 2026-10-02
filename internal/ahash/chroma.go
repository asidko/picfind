package ahash

import "image"

func jpegRGB(y, cb, cr int) (uint8, uint8, uint8) {
	cb, cr = cb-128, cr-128
	r := y + ((91881*cr + 32768) >> 16)
	g := y + ((-22554*cb - 46802*cr + 32768) >> 16)
	b := y + ((116130*cb + 32768) >> 16)
	return clamp8(r), clamp8(g), clamp8(b)
}

func clamp8(v int) uint8 {
	return uint8(min(max(v, 0), 255))
}

func multHi(v, coeff int) int {
	return (v * coeff) >> 8
}

func vp8Clip(v int) uint8 {
	if v&^16383 == 0 {
		return uint8(v >> 6)
	}
	if v < 0 {
		return 0
	}
	return 255
}

func webpRGB(y, u, v int) (uint8, uint8, uint8) {
	yy := multHi(y, 19077)
	r := vp8Clip(yy + multHi(v, 26149) - 14234)
	g := vp8Clip(yy - multHi(u, 6419) - multHi(v, 13320) + 8708)
	b := vp8Clip(yy + multHi(u, 33050) - 17685)
	return r, g, b
}

func ycbcrRows(src *image.YCbCr, format string) rowFunc {
	if format == "webp" && src.SubsampleRatio == image.YCbCrSubsampleRatio420 {
		return webpRows(src)
	}
	return jpegRows(src)
}

func chromaShift(ratio image.YCbCrSubsampleRatio) (x, y uint) {
	switch ratio {
	case image.YCbCrSubsampleRatio422:
		return 1, 0
	case image.YCbCrSubsampleRatio420:
		return 1, 1
	case image.YCbCrSubsampleRatio440:
		return 0, 1
	case image.YCbCrSubsampleRatio411:
		return 2, 0
	case image.YCbCrSubsampleRatio410:
		return 2, 1
	}
	return 0, 0
}

func jpegRows(src *image.YCbCr) rowFunc {
	b := src.Bounds()
	sx, sy := chromaShift(src.SubsampleRatio)
	x0 := b.Min.X >> sx
	return func(y int, dst []uint8) {
		y += b.Min.Y
		yr := src.Y[src.YOffset(b.Min.X, y):][:len(dst)]
		c := (y>>sy - b.Min.Y>>sy) * src.CStride
		cb, cr := src.Cb[c:], src.Cr[c:]
		for x := range dst {
			i := (b.Min.X+x)>>sx - x0
			dst[x] = luma(jpegRGB(int(yr[x]), int(cb[i]), int(cr[i])))
		}
	}
}

func webpRows(src *image.YCbCr) rowFunc {
	b := src.Bounds()
	cw, ch := (b.Dx()+1)/2, (b.Dy()+1)/2
	chromaRow := func(plane []uint8, y int) []uint8 {
		return plane[min(max(y, 0), ch-1)*src.CStride:][:cw]
	}
	u, v := make([]uint8, b.Dx()), make([]uint8, b.Dx())
	return func(y int, dst []uint8) {
		near, far := y/2, y/2+1
		if y%2 == 0 {
			far = y/2 - 1
		}
		fancyUpsample(u, chromaRow(src.Cb, near), chromaRow(src.Cb, far))
		fancyUpsample(v, chromaRow(src.Cr, near), chromaRow(src.Cr, far))
		yr := src.Y[src.YOffset(b.Min.X, b.Min.Y+y):][:len(dst)]
		for x := range dst {
			dst[x] = luma(webpRGB(int(yr[x]), int(u[x]), int(v[x])))
		}
	}
}

func fancyUpsample(out, near, far []uint8) {
	for x := range out {
		a, b := x/2, x/2+1
		if x%2 == 0 {
			b = x/2 - 1
		}
		na, fa := int(near[a]), int(far[a])
		if x == 0 || b >= len(near) {
			out[x] = uint8((3*na + fa + 2) >> 2)
		} else {
			out[x] = uint8((((na + 3*int(near[b]) + 3*fa + int(far[b]) + 8) >> 3) + na) >> 1)
		}
	}
}
