package meme

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"omni-bot/internal/store"
)

type stubTagger struct {
	calls      int
	lastMIME   string
	lastAnim   bool
	lastHint   string
	lastBytes  []byte
	tagBuilder func(isAnimated bool, hint string) VisionTag
}

func (s *stubTagger) TagMeme(_ context.Context, imageBytes []byte, mimeType string, isAnimated bool, hint string) (VisionTag, error) {
	s.calls++
	s.lastBytes = append([]byte(nil), imageBytes...)
	s.lastMIME = mimeType
	s.lastAnim = isAnimated
	s.lastHint = hint
	if s.tagBuilder != nil {
		return s.tagBuilder(isAnimated, hint), nil
	}
	return VisionTag{
		OverlayText: "笑死",
		Description: "一只大笑的猫",
		Emotions:    "开心, 爆笑",
		Scenes:      "吐槽, 接梗",
	}, nil
}

type stubEmbedder struct {
	calls int
	fn    func(text string) []float32
}

func (s *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	s.calls++
	if s.fn != nil {
		return s.fn(text), nil
	}
	return makeUnitVec(1024, 0), nil
}

func makeUnitVec(dim, axis int) []float32 {
	v := make([]float32, dim)
	if axis >= 0 && axis < dim {
		v[axis] = 1.0
	}
	return v
}

func makeGradientPNG(t *testing.T, w, h int, horizontal bool, invert bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			var v uint8
			if horizontal {
				v = uint8((x * 255) / max(1, w-1))
			} else {
				v = uint8((y * 255) / max(1, h-1))
			}
			if invert {
				v = 255 - v
			}
			// Add a subtle diagonal wave so gradients have rich horizontal transitions.
			wave := uint8(((x + y*2) % 32) * 2)
			r := uint8((int(v)*3 + int(wave)) / 4)
			img.SetRGBA(x, y, color.RGBA{R: r, G: v, B: uint8((int(v) + 20) % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func reencodeJPEG(t *testing.T, pngBytes []byte, quality int) []byte {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func makeAnimatedGIF(t *testing.T, frames int) []byte {
	t.Helper()
	palette := color.Palette{
		color.RGBA{R: 20, G: 20, B: 20, A: 255},
		color.RGBA{R: 230, G: 40, B: 40, A: 255},
		color.RGBA{R: 40, G: 210, B: 60, A: 255},
		color.RGBA{R: 40, G: 90, B: 240, A: 255},
		color.RGBA{R: 240, G: 210, B: 40, A: 255},
		color.RGBA{R: 250, G: 250, B: 250, A: 255},
	}
	g := &gif.GIF{
		Config: image.Config{
			ColorModel: palette,
			Width:      64,
			Height:     64,
		},
	}
	for f := range frames {
		img := image.NewPaletted(image.Rect(0, 0, 64, 64), palette)
		bgIdx := uint8((f % 4) + 1)
		for y := range 64 {
			for x := range 64 {
				if x > 8+f*4 && x < 40+f*3 && y > 10 && y < 54 {
					img.SetColorIndex(x, y, 5)
				} else {
					img.SetColorIndex(x, y, bgIdx)
				}
			}
		}
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 10)
		g.Disposal = append(g.Disposal, gif.DisposalBackground)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

func openTestVault(t *testing.T, tagger VisionTagger, embedder Embedder) (*Vault, *store.Store, *store.BlobStore) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(t.Context(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	blobs, err := store.NewBlobStore(filepath.Join(dir, "media"))
	if err != nil {
		t.Fatalf("new blob store: %v", err)
	}
	vault, err := NewVault(Options{
		Store:    st,
		Blobs:    blobs,
		Tagger:   tagger,
		Embedder: embedder,
	})
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	return vault, st, blobs
}

func TestComputeDHashAndDedupDistance(t *testing.T) {
	imgA := makeGradientPNG(t, 128, 128, true, false)
	imgAJPEG := reencodeJPEG(t, imgA, 78)
	imgInverted := makeGradientPNG(t, 128, 128, true, true)

	hA, hexA, err := ComputeDHashBytes(imgA)
	if err != nil {
		t.Fatalf("dhash A: %v", err)
	}
	if len(hexA) != 16 {
		t.Fatalf("expected 16-char hex phash, got %q", hexA)
	}
	parsedA, err := ParsePHash(hexA)
	if err != nil || parsedA != hA {
		t.Fatalf("roundtrip ParsePHash failed: got %x, err=%v", parsedA, err)
	}

	hAJPEG, _, err := ComputeDHashBytes(imgAJPEG)
	if err != nil {
		t.Fatalf("dhash A jpeg: %v", err)
	}
	if dist := HammingDistance(hA, hAJPEG); dist > DefaultDedupDistance {
		t.Fatalf("expected re-encoded JPEG within dedup distance %d, got %d", DefaultDedupDistance, dist)
	}

	hInv, _, err := ComputeDHashBytes(imgInverted)
	if err != nil {
		t.Fatalf("dhash inverted: %v", err)
	}
	if dist := HammingDistance(hA, hInv); dist <= DefaultDedupDistance {
		t.Fatalf("expected inverted image distance > %d, got %d", DefaultDedupDistance, dist)
	}
}

func TestBuildGIFContactSheet(t *testing.T) {
	gifBytes := makeAnimatedGIF(t, 6)
	sheetBytes, sampled, err := BuildGIFContactSheet(gifBytes, 128)
	if err != nil {
		t.Fatalf("BuildGIFContactSheet: %v", err)
	}
	if sampled != 4 {
		t.Fatalf("expected 4 sampled frames, got %d", sampled)
	}
	img, format, err := image.Decode(bytes.NewReader(sheetBytes))
	if err != nil {
		t.Fatalf("decode contact sheet: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("expected jpeg contact sheet, got %q", format)
	}
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
		t.Fatalf("expected 256x256 2x2 contact sheet, got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestVaultIngestDedupAndAnimatedGIF(t *testing.T) {
	tagger := &stubTagger{}
	embedder := &stubEmbedder{}
	vault, st, blobs := openTestVault(t, tagger, embedder)
	ctx := t.Context()

	pngBytes := makeGradientPNG(t, 160, 120, true, false)
	res, err := vault.Ingest(ctx, pngBytes, ".png", "群友发来的猫图")
	if err != nil {
		t.Fatalf("ingest png: %v", err)
	}
	if res.Status != IngestStatusIngested {
		t.Fatalf("expected ingested, got %q", res.Status)
	}
	if res.Meme.EmbeddingDim != 1024 || len(res.Meme.Embedding) != 1024 {
		t.Fatalf("expected 1024-dim embedding, got dim=%d len=%d", res.Meme.EmbeddingDim, len(res.Meme.Embedding))
	}
	if tagger.calls != 1 || tagger.lastAnim {
		t.Fatalf("unexpected tagger state: calls=%d anim=%v", tagger.calls, tagger.lastAnim)
	}

	// Verify both raw file and thumbnail exist in BlobStore.
	for _, rel := range []string{res.Meme.RelPath, res.Meme.ThumbRelPath} {
		rc, err := blobs.OpenMemeFile(ctx, rel)
		if err != nil {
			t.Fatalf("open stored meme file %q: %v", rel, err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		if len(data) == 0 {
			t.Fatalf("stored file %q is empty", rel)
		}
	}

	// Exact duplicate should short-circuit without calling vision tagger.
	dupExact, err := vault.Ingest(ctx, pngBytes, ".png", "")
	if err != nil {
		t.Fatalf("ingest exact dup: %v", err)
	}
	if dupExact.Status != IngestStatusDuplicateHash || dupExact.DuplicateOf != res.Meme.Hash {
		t.Fatalf("expected duplicate_hash of %s, got %+v", res.Meme.Hash, dupExact)
	}
	if tagger.calls != 1 {
		t.Fatalf("tagger should not be called on exact duplicate, calls=%d", tagger.calls)
	}

	// Re-encoded JPEG of same image should be caught by pHash Hamming distance <= 6.
	jpegVariant := reencodeJPEG(t, pngBytes, 80)
	dupPHash, err := vault.Ingest(ctx, jpegVariant, ".jpg", "")
	if err != nil {
		t.Fatalf("ingest phash dup: %v", err)
	}
	if dupPHash.Status != IngestStatusDuplicatePHash || dupPHash.DuplicateOf != res.Meme.Hash {
		t.Fatalf("expected duplicate_phash of %s, got %+v", res.Meme.Hash, dupPHash)
	}
	if tagger.calls != 1 {
		t.Fatalf("tagger should not be called on phash duplicate, calls=%d", tagger.calls)
	}

	// Multi-frame GIF should build a 2x2 contact sheet JPEG for vision tagging.
	gifBytes := makeAnimatedGIF(t, 5)
	gifRes, err := vault.Ingest(ctx, gifBytes, ".gif", "动图表情")
	if err != nil {
		t.Fatalf("ingest gif: %v", err)
	}
	if gifRes.Status != IngestStatusIngested {
		t.Fatalf("expected gif ingested, got %q", gifRes.Status)
	}
	if !tagger.lastAnim || tagger.lastMIME != "image/jpeg" {
		t.Fatalf("expected animated jpeg contact sheet sent to tagger, got anim=%v mime=%q", tagger.lastAnim, tagger.lastMIME)
	}
	sheetImg, format, err := image.Decode(bytes.NewReader(tagger.lastBytes))
	if err != nil || format != "jpeg" || sheetImg.Bounds().Dx() != 512 || sheetImg.Bounds().Dy() != 512 {
		t.Fatalf("unexpected contact sheet sent to tagger: format=%q bounds=%v err=%v", format, sheetImg.Bounds(), err)
	}

	// Verify meme_seen recorded approval.
	seen, ok, err := st.GetMemeSeen(ctx, gifRes.Meme.Hash)
	if err != nil || !ok || seen.Status != "approved" {
		t.Fatalf("expected approved meme_seen, got ok=%v seen=%+v err=%v", ok, seen, err)
	}
}

func TestVaultRetrieveHandUpdateRecordUseAndBlacklistDelete(t *testing.T) {
	embedder := &stubEmbedder{
		fn: func(text string) []float32 {
			switch {
			case strings.Contains(text, "猫") || strings.Contains(text, "无语"):
				return makeUnitVec(1024, 1)
			case strings.Contains(text, "狗") || strings.Contains(text, "开心"):
				return makeUnitVec(1024, 2)
			default:
				return makeUnitVec(1024, 3)
			}
		},
	}
	tagger := &stubTagger{
		tagBuilder: func(_ bool, hint string) VisionTag {
			if hint == "cat" {
				return VisionTag{
					OverlayText: "就这",
					Description: "白眼翻上天的无语猫",
					Emotions:    "无语, 嫌弃",
					Scenes:      "敷衍, 冷场",
				}
			}
			return VisionTag{
				OverlayText: "冲鸭",
				Description: "吐舌头狂奔的开心小狗",
				Emotions:    "开心, 兴奋",
				Scenes:      "庆祝, 出发",
			}
		},
	}
	vault, st, _ := openTestVault(t, tagger, embedder)
	ctx := t.Context()

	catPNG := makeGradientPNG(t, 96, 96, true, false)
	dogPNG := makeGradientPNG(t, 96, 96, true, true)

	catRes, err := vault.Ingest(ctx, catPNG, ".png", "cat")
	if err != nil {
		t.Fatalf("ingest cat: %v", err)
	}
	dogRes, err := vault.Ingest(ctx, dogPNG, ".png", "dog")
	if err != nil {
		t.Fatalf("ingest dog: %v", err)
	}

	// Hybrid search targeting the dog meme via vector + keyword.
	items, formatted, err := vault.RetrieveHand(ctx, "好开心 庆祝", makeUnitVec(1024, 2), 2)
	if err != nil {
		t.Fatalf("RetrieveHand: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 hand items, got %d", len(items))
	}
	if items[0].Meme.Hash != dogRes.Meme.Hash {
		t.Fatalf("expected dog meme ranked first, got %s", items[0].Meme.Hash)
	}
	dogPrefix := "[meme:#" + ShortHash(dogRes.Meme.Hash) + "]"
	if !strings.HasPrefix(formatted, "<available_memes>\n") || !strings.HasSuffix(formatted, "</available_memes>") {
		t.Fatalf("unexpected formatted hand wrapper:\n%s", formatted)
	}
	if !strings.Contains(formatted, dogPrefix) {
		t.Fatalf("expected %s in formatted hand:\n%s", dogPrefix, formatted)
	}

	// UpdateImpression using short #prefix.
	newDesc := "超激动的撒欢金毛"
	newScenes := "开黑胜利, 抽奖欧皇"
	updated, err := vault.UpdateImpression(ctx, "#"+ShortHash(dogRes.Meme.Hash), ImpressionPatch{
		Description: &newDesc,
		Scenes:      &newScenes,
	})
	if err != nil {
		t.Fatalf("UpdateImpression: %v", err)
	}
	if updated.Description != newDesc || updated.Scenes != newScenes {
		t.Fatalf("impression not updated: %+v", updated)
	}

	// RecordUse increments use_count.
	if err := vault.RecordUse(ctx, ShortHash(catRes.Meme.Hash)); err != nil {
		t.Fatalf("RecordUse: %v", err)
	}
	gotCat, ok, err := st.GetMeme(ctx, catRes.Meme.Hash)
	if err != nil || !ok || gotCat.UseCount != 1 {
		t.Fatalf("expected use_count=1, got ok=%v count=%d err=%v", ok, gotCat.UseCount, err)
	}

	// Delete blacklists the meme in meme_seen so re-ingestion is blocked.
	if _, err := vault.Delete(ctx, "#"+ShortHash(catRes.Meme.Hash)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	seen, ok, err := st.GetMemeSeen(ctx, catRes.Meme.Hash)
	if err != nil || !ok || seen.Status != store.MemeStatusDeleted {
		t.Fatalf("expected deleted status in meme_seen, got ok=%v seen=%+v err=%v", ok, seen, err)
	}

	reingest, err := vault.Ingest(ctx, catPNG, ".png", "cat again")
	if err != nil {
		t.Fatalf("reingest deleted meme: %v", err)
	}
	if reingest.Status != IngestStatusBlacklisted {
		t.Fatalf("expected blacklisted on re-ingest of deleted meme, got %q", reingest.Status)
	}

	// Near-duplicate variant of deleted meme is also blocked.
	reingestVariant, err := vault.Ingest(ctx, reencodeJPEG(t, catPNG, 85), ".jpg", "cat jpeg")
	if err != nil {
		t.Fatalf("reingest deleted variant: %v", err)
	}
	if reingestVariant.Status != IngestStatusBlacklisted {
		t.Fatalf("expected blacklisted on near-duplicate of deleted meme, got %q", reingestVariant.Status)
	}

	// RetrieveHand no longer returns deleted cat meme.
	afterDelete, _, err := vault.RetrieveHand(ctx, "无语猫", makeUnitVec(1024, 1), 5)
	if err != nil {
		t.Fatalf("RetrieveHand after delete: %v", err)
	}
	for _, it := range afterDelete {
		if it.Meme.Hash == catRes.Meme.Hash {
			t.Fatalf("deleted meme still returned in RetrieveHand")
		}
	}
}

func TestVaultRejectsNonMeme(t *testing.T) {
	isMeme := false
	tagger := &stubTagger{
		tagBuilder: func(_ bool, _ string) VisionTag {
			return VisionTag{IsMeme: &isMeme, RejectReason: "screenshot"}
		},
	}
	vault, _, _ := openTestVault(t, tagger, nil)
	ctx := t.Context()

	raw := makeGradientPNG(t, 80, 80, false, false)
	res, err := vault.Ingest(ctx, raw, ".png", "")
	if err != nil {
		t.Fatalf("ingest rejected: %v", err)
	}
	if res.Status != IngestStatusRejected {
		t.Fatalf("expected rejected, got %q", res.Status)
	}

	res2, err := vault.Ingest(ctx, raw, ".png", "")
	if err != nil {
		t.Fatalf("reingest rejected: %v", err)
	}
	if res2.Status != IngestStatusBlacklisted {
		t.Fatalf("expected blacklisted on second attempt of rejected image, got %q", res2.Status)
	}
	if tagger.calls != 1 {
		t.Fatalf("tagger should not be re-invoked for previously rejected image, calls=%d", tagger.calls)
	}
}
