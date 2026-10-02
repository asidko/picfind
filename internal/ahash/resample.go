package ahash

import "math"

const precisionBits = 32 - 8 - 2

type rowFunc func(y int, dst []uint8)

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	x *= math.Pi
	return math.Sin(x) / x
}

func lanczos(x float64) float64 {
	if -3 <= x && x < 3 {
		return sinc(x) * sinc(x/3)
	}
	return 0
}

type kernel struct {
	size   int
	bounds []int
	coeffs []int
}

func identityKernel(n int) kernel {
	k := kernel{size: 1, bounds: make([]int, n*2), coeffs: make([]int, n)}
	for i := range n {
		k.bounds[i*2], k.bounds[i*2+1], k.coeffs[i] = i, 1, 1<<precisionBits
	}
	return k
}

func newKernel(inSize, outSize int) kernel {
	if inSize == outSize {
		return identityKernel(outSize)
	}
	scale := float64(inSize) / float64(outSize)
	filterScale := max(scale, 1)
	support := 3 * filterScale
	inv := 1 / filterScale
	k := kernel{size: int(math.Ceil(support))*2 + 1, bounds: make([]int, outSize*2)}
	k.coeffs = make([]int, outSize*k.size)
	weights := make([]float64, k.size)
	for xx := range outSize {
		center := (float64(xx) + 0.5) * scale
		lo := max(int(center-support+0.5), 0)
		hi := min(int(center+support+0.5), inSize) - lo
		total := 0.0
		for x := range hi {
			weights[x] = lanczos((float64(x+lo) - center + 0.5) * inv)
			total += weights[x]
		}
		for x := range hi {
			w := weights[x]
			if total != 0 {
				w /= total
			}
			if w < 0 {
				k.coeffs[xx*k.size+x] = int(-0.5 + float64(w*(1<<precisionBits)))
			} else {
				k.coeffs[xx*k.size+x] = int(0.5 + float64(w*(1<<precisionBits)))
			}
		}
		k.bounds[xx*2], k.bounds[xx*2+1] = lo, hi
	}
	return k
}

func clip8(v int) uint8 {
	if v >= 1<<(precisionBits+8) {
		return 255
	}
	if v <= 0 {
		return 0
	}
	return uint8(v >> precisionBits)
}

func (k kernel) apply(src []uint8, i int) uint8 {
	lo, n := k.bounds[i*2], k.bounds[i*2+1]
	c := k.coeffs[i*k.size:][:n]
	s := src[lo:][:len(c)]
	ss := 1 << (precisionBits - 1)
	for j, v := range c {
		ss += int(s[j]) * v
	}
	return clip8(ss)
}

func resample(w, h int, rows rowFunc) []uint8 {
	horiz, vert := newKernel(w, size), newKernel(h, size)
	first := vert.bounds[0]
	n := vert.bounds[size*2-2] + vert.bounds[size*2-1] - first
	for i := range size {
		vert.bounds[i*2] -= first
	}
	row := make([]uint8, w)
	cols := make([]uint8, size*n)
	for y := range n {
		rows(first+y, row)
		for x := range size {
			cols[x*n+y] = horiz.apply(row, x)
		}
	}
	out := make([]uint8, size*size)
	for x := range size {
		col := cols[x*n : (x+1)*n]
		for y := range size {
			out[y*size+x] = vert.apply(col, y)
		}
	}
	return out
}
