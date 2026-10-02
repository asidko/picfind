package ahash

func colorTableSize(flags byte) int {
	if flags&0x80 == 0 {
		return 0
	}
	return 1 << (flags&7 + 1)
}

func skipSubBlocks(gif []byte, i int) int {
	for i < len(gif) && gif[i] != 0 {
		i += int(gif[i]) + 1
	}
	return i + 1
}

func keepTransparentColor(gif []byte) {
	if len(gif) < 13 {
		return
	}
	global := colorTableSize(gif[10])
	var gces []int
	for i := 13 + 3*global; i+1 < len(gif); {
		switch gif[i] {
		case 0x21:
			if gif[i+1] == 0xf9 && i+6 < len(gif) && gif[i+2] == 4 {
				gces = append(gces, i+3)
			}
			i = skipSubBlocks(gif, i+2)
		case 0x2c:
			if i+9 >= len(gif) {
				return
			}
			colors := global
			if local := colorTableSize(gif[i+9]); local > 0 {
				colors = local
			}
			for _, p := range gces {
				if int(gif[p+3]) < colors {
					gif[p] &^= 1
				}
			}
			return
		default:
			return
		}
	}
}
