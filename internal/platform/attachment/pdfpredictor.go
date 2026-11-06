package attachment

// undoPredictor reverses the PNG (10-15) and TIFF (2) predictors of a Flate
// stream so names hidden behind them are visible to the scan. Parameters are
// clamped, never refused: a predictor alone is no reason to reject a PDF.
func undoPredictor(data []byte, s streamSpec) []byte {
	colors := clamp(s.colors, 1, 32, 1)
	columns := clamp(s.columns, 1, 1<<24, 1)
	bpc := s.bpc
	switch bpc {
	case 1, 2, 4, 8, 16:
	default:
		bpc = 8
	}
	rowBits := colors * bpc * columns
	rowBytes := (rowBits + 7) / 8
	switch {
	case s.predictor >= 10 && s.predictor <= 15:
		return undoPNG(data, rowBytes, max(1, colors*bpc/8))
	case s.predictor == 2:
		return undoTIFF(data, rowBytes, colors, bpc)
	}
	return data
}

func clamp(v, lo, hi, def int) int {
	if v < lo || v > hi {
		return def
	}
	return v
}

func undoPNG(data []byte, rowBytes, bpp int) []byte {
	out := make([]byte, 0, len(data))
	var prev []byte
	for off := 0; off < len(data); off += rowBytes + 1 {
		tag := data[off]
		row := append([]byte(nil), data[off+1:min(len(data), off+1+rowBytes)]...)
		for i := range row {
			var a, b, c byte
			if i >= bpp {
				a = row[i-bpp]
			}
			if i < len(prev) {
				b = prev[i]
			}
			if i >= bpp && i < len(prev) {
				c = prev[i-bpp]
			}
			switch tag {
			case 1:
				row[i] += a
			case 2:
				row[i] += b
			case 3:
				row[i] += byte((int(a) + int(b)) / 2)
			case 4:
				row[i] += paeth(a, b, c)
			}
		}
		out = append(out, row...)
		prev = row
	}
	return out
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// undoTIFF adds each sample to the same component of the previous pixel,
// modulo 2^bpc, row by row.
func undoTIFF(data []byte, rowBytes, colors, bpc int) []byte {
	out := append([]byte(nil), data...)
	for off := 0; off < len(out); off += rowBytes {
		row := out[off:min(len(out), off+rowBytes)]
		switch bpc {
		case 8:
			for i := colors; i < len(row); i++ {
				row[i] += row[i-colors]
			}
		case 16:
			for i := colors * 2; i+1 < len(row); i += 2 {
				v := (uint16(row[i])<<8 | uint16(row[i+1])) + (uint16(row[i-colors*2])<<8 | uint16(row[i-colors*2+1]))
				row[i], row[i+1] = byte(v>>8), byte(v&0xff)
			}
		default: // 1, 2 or 4 bits per sample
			mask := byte(1<<bpc - 1)
			samples := len(row) * 8 / bpc
			get := func(n int) byte {
				bit := n * bpc
				return row[bit/8] >> (8 - bpc - bit%8) & mask
			}
			for n := colors; n < samples; n++ {
				v := (get(n) + get(n-colors)) & mask
				bit := n * bpc
				shift := 8 - bpc - bit%8
				row[bit/8] = row[bit/8]&^(mask<<shift) | v<<shift
			}
		}
	}
	return out
}
