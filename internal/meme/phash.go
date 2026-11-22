package meme

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math/bits"
	"strings"
)

const DefaultDedupDistance = 6

// ComputeDHash computes a 64-bit difference hash (9x8 grayscale horizontal gradient) in pure Go.
func ComputeDHash(img image.Image) (uint64, string) {
	gray := resizeGray(img, 9, 8)
	var hash uint64
	bit := 63
	for y := range 8 {
		rowOffset := y * 9
		for x := range 8 {
			if gray[rowOffset+x] < gray[rowOffset+x+1] {
				hash |= 1 << bit
			}
			bit--
		}
	}
	return hash, FormatPHash(hash)
}

// ComputeDHashBytes decodes an image (PNG, JPEG, or GIF) and computes its 64-bit dHash.
func ComputeDHashBytes(raw []byte) (uint64, string, error) {
	if len(raw) == 0 {
		return 0, "", errors.New("meme: empty image data")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return 0, "", fmt.Errorf("meme: decode image for dhash: %w", err)
	}
	h, hexStr := ComputeDHash(img)
	return h, hexStr, nil
}

func FormatPHash(h uint64) string {
	return fmt.Sprintf("%016x", h)
}

func ParsePHash(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if len(s) != 16 {
		return 0, fmt.Errorf("meme: invalid phash length %d", len(s))
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("meme: parse phash %q: %w", s, err)
	}
	var out uint64
	for _, b := range raw {
		out = (out << 8) | uint64(b)
	}
	return out, nil
}

func HammingDistance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

func HammingDistanceHex(a, b string) (int, error) {
	ha, err := ParsePHash(a)
	if err != nil {
		return 0, err
	}
	hb, err := ParsePHash(b)
	if err != nil {
		return 0, err
	}
	return HammingDistance(ha, hb), nil
}

// resizeGray downsamples or upsamples img into a dstW x dstH BT.601 luminance grid.
func resizeGray(img image.Image, dstW, dstH int) []uint8 {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	out := make([]uint8, dstW*dstH)
	if srcW <= 0 || srcH <= 0 {
		return out
	}

	for dy := range dstH {
		y0 := bounds.Min.Y + (dy*srcH)/dstH
		y1 := bounds.Min.Y + ((dy+1)*srcH)/dstH
		if y1 <= y0 {
			y1 = min(y0+1, bounds.Max.Y)
		}
		for dx := range dstW {
			x0 := bounds.Min.X + (dx*srcW)/dstW
			x1 := bounds.Min.X + ((dx+1)*srcW)/dstW
			if x1 <= x0 {
				x1 = min(x0+1, bounds.Max.X)
			}

			var sum uint64
			var count uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, b, _ := img.At(sx, sy).RGBA()
					// BT.601 luma on 16-bit channels, scaled down to 8-bit.
					luma := (299*uint64(r) + 587*uint64(g) + 114*uint64(b)) / 1000
					sum += luma >> 8
					count++
				}
			}
			if count > 0 {
				out[dy*dstW+dx] = uint8(sum / count)
			}
		}
	}
	return out
}
