package meme

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"net/http"
)

const (
	defaultThumbMaxEdge = 256
	defaultGridTileSize = 256
	defaultJPEGQuality  = 82
)

// RenderThumbnail decodes an image and encodes an aspect-preserving JPEG thumbnail bounded by maxEdge.
func RenderThumbnail(raw []byte, maxEdge int) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("meme: empty image data")
	}
	if maxEdge <= 0 {
		maxEdge = defaultThumbMaxEdge
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("meme: decode image for thumbnail: %w", err)
	}
	resized := resizeFitRGBA(img, maxEdge, maxEdge)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: defaultJPEGQuality}); err != nil {
		return nil, fmt.Errorf("meme: encode thumbnail jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// BuildVisionImage prepares an image payload for vision tagging.
// Multi-frame GIFs are sampled up to 4 frames and composited into a 2x2 JPEG contact sheet.
func BuildVisionImage(raw []byte) (data []byte, mimeType string, isAnimated bool, err error) {
	if len(raw) == 0 {
		return nil, "", false, errors.New("meme: empty image data")
	}
	detected := http.DetectContentType(raw)
	if detected == "image/gif" {
		g, gerr := gif.DecodeAll(bytes.NewReader(raw))
		if gerr != nil {
			return nil, "", false, fmt.Errorf("meme: decode gif: %w", gerr)
		}
		if len(g.Image) > 1 {
			sheet, _, serr := buildContactSheetFromGIF(g, defaultGridTileSize)
			if serr != nil {
				return nil, "", false, serr
			}
			return sheet, "image/jpeg", true, nil
		}
		if len(g.Image) == 1 {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, toRGBA(g.Image[0]), &jpeg.Options{Quality: defaultJPEGQuality}); err != nil {
				return nil, "", false, fmt.Errorf("meme: encode single-frame gif: %w", err)
			}
			return buf.Bytes(), "image/jpeg", false, nil
		}
	}

	if _, format, derr := image.Decode(bytes.NewReader(raw)); derr != nil {
		return nil, "", false, fmt.Errorf("meme: decode image: %w", derr)
	} else {
		switch format {
		case "png":
			return raw, "image/png", false, nil
		case "jpeg":
			return raw, "image/jpeg", false, nil
		default:
			thumb, terr := RenderThumbnail(raw, 1024)
			if terr != nil {
				return nil, "", false, terr
			}
			return thumb, "image/jpeg", false, nil
		}
	}
}

// BuildGIFContactSheet samples up to 4 frames from an animated GIF and renders a 2x2 JPEG contact sheet.
func BuildGIFContactSheet(raw []byte, tileSize int) ([]byte, int, error) {
	if len(raw) == 0 {
		return nil, 0, errors.New("meme: empty gif data")
	}
	g, err := gif.DecodeAll(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, fmt.Errorf("meme: decode gif: %w", err)
	}
	return buildContactSheetFromGIF(g, tileSize)
}

func buildContactSheetFromGIF(g *gif.GIF, tileSize int) ([]byte, int, error) {
	if len(g.Image) == 0 {
		return nil, 0, errors.New("meme: gif has no frames")
	}
	if tileSize <= 0 {
		tileSize = defaultGridTileSize
	}

	indices := sampleFrameIndices(len(g.Image), 4)
	wanted := make(map[int]int, len(indices))
	for slot, idx := range indices {
		wanted[idx] = slot
	}

	canvasW, canvasH := g.Config.Width, g.Config.Height
	if canvasW <= 0 || canvasH <= 0 {
		b := g.Image[0].Bounds()
		canvasW, canvasH = b.Dx(), b.Dy()
	}
	if canvasW <= 0 || canvasH <= 0 {
		return nil, 0, errors.New("meme: invalid gif canvas dimensions")
	}

	canvas := image.NewRGBA(image.Rect(0, 0, canvasW, canvasH))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)

	sampled := make([]*image.RGBA, len(indices))
	for i, frame := range g.Image {
		var prev *image.RGBA
		disposal := byte(0)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		if disposal == gif.DisposalPrevious {
			prev = cloneRGBA(canvas)
		}

		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		if slot, ok := wanted[i]; ok {
			sampled[slot] = cloneRGBA(canvas)
		}

		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, frame.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			if prev != nil {
				draw.Draw(canvas, canvas.Bounds(), prev, image.Point{}, draw.Src)
			}
		}
	}

	sheet := image.NewRGBA(image.Rect(0, 0, tileSize*2, tileSize*2))
	draw.Draw(sheet, sheet.Bounds(), &image.Uniform{C: color.RGBA{R: 245, G: 245, B: 245, A: 255}}, image.Point{}, draw.Src)

	for slot, frame := range sampled {
		if frame == nil {
			continue
		}
		fitted := resizeFitRGBA(frame, tileSize, tileSize)
		col := slot % 2
		row := slot / 2
		cellX := col * tileSize
		cellY := row * tileSize
		offX := cellX + (tileSize-fitted.Bounds().Dx())/2
		offY := cellY + (tileSize-fitted.Bounds().Dy())/2
		dstRect := image.Rect(offX, offY, offX+fitted.Bounds().Dx(), offY+fitted.Bounds().Dy())
		draw.Draw(sheet, dstRect, fitted, image.Point{}, draw.Src)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, sheet, &jpeg.Options{Quality: defaultJPEGQuality}); err != nil {
		return nil, 0, fmt.Errorf("meme: encode contact sheet jpeg: %w", err)
	}
	return buf.Bytes(), len(indices), nil
}

func sampleFrameIndices(total, maxSamples int) []int {
	if total <= 0 || maxSamples <= 0 {
		return nil
	}
	if total <= maxSamples {
		out := make([]int, total)
		for i := range total {
			out[i] = i
		}
		return out
	}
	out := make([]int, maxSamples)
	for i := range maxSamples {
		out[i] = (i * (total - 1)) / (maxSamples - 1)
	}
	return out
}

func resizeFitRGBA(img image.Image, maxW, maxH int) *image.RGBA {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	dstW, dstH := srcW, srcH
	if dstW > maxW {
		dstH = max(1, (dstH*maxW)/dstW)
		dstW = maxW
	}
	if dstH > maxH {
		dstW = max(1, (dstW*maxH)/dstH)
		dstH = maxH
	}
	return resizeRGBA(img, dstW, dstH)
}

func resizeRGBA(img image.Image, dstW, dstH int) *image.RGBA {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	if srcW <= 0 || srcH <= 0 {
		return dst
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

			var sr, sg, sb, sa, count uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, b, a := img.At(sx, sy).RGBA()
					sr += uint64(r >> 8)
					sg += uint64(g >> 8)
					sb += uint64(b >> 8)
					sa += uint64(a >> 8)
					count++
				}
			}
			if count > 0 {
				dst.SetRGBA(dx, dy, color.RGBA{
					R: uint8(sr / count),
					G: uint8(sg / count),
					B: uint8(sb / count),
					A: uint8(sa / count),
				})
			}
		}
	}
	return dst
}

func toRGBA(img image.Image) *image.RGBA {
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

func cloneRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}
