// Command icongen rasterizes internal/assets/icon.svg — the single source of
// truth for the Task Timer app icon — into every platform format we ship.
//
// Run it from the repository root:
//
//	go run ./tools/icongen
//
// Outputs:
//
//	internal/assets/icon.png        512x512, embedded at runtime (committed)
//	build/icons/png/icon_<N>.png    N in 16,32,48,64,128,256,512,1024
//	build/icons/TaskTimer.icns      macOS bundle icon
//	build/icons/TaskTimer.ico       Windows installer + shortcut icon
//
// Every size is rendered natively from the SVG rather than downscaled from a
// single large raster: downscaling turns the small sizes to mush.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

const (
	svgPath     = "internal/assets/icon.svg"
	embedPNG    = "internal/assets/icon.png"
	embedSize   = 512
	iconsDir    = "build/icons"
	pngDir      = "build/icons/png"
	icnsPath    = "build/icons/TaskTimer.icns"
	icoPath     = "build/icons/TaskTimer.ico"
	pngDirPerm  = 0o755
	pngFilePerm = 0o644
)

// pngSizes are the standalone PNGs emitted under build/icons/png.
var pngSizes = []int{16, 32, 48, 64, 128, 256, 512, 1024}

// icnsChunks maps an ICNS OSType to the pixel size it carries. Only the
// PNG-capable types are used, so each chunk payload is a plain PNG.
var icnsChunks = []struct {
	osType string
	size   int
}{
	{"icp4", 16},
	{"icp5", 32},
	{"ic07", 128},
	{"ic08", 256},
	{"ic09", 512},
	{"ic10", 1024},
}

// icoSizes are the images packed into the multi-resolution .ico.
var icoSizes = []int{16, 32, 48, 64, 128, 256}

func main() {
	if err := run(); err != nil {
		log.Fatalf("icongen: %v", err)
	}
}

func run() error {
	svg, err := os.ReadFile(svgPath)
	if err != nil {
		return fmt.Errorf("read source svg: %w", err)
	}

	if err := os.MkdirAll(pngDir, pngDirPerm); err != nil {
		return fmt.Errorf("create png dir: %w", err)
	}
	if err := os.MkdirAll(iconsDir, pngDirPerm); err != nil {
		return fmt.Errorf("create icons dir: %w", err)
	}

	// Encode each size once and reuse the bytes across every container.
	encoded := make(map[int][]byte)
	for _, size := range allSizes() {
		img, err := rasterize(svg, size)
		if err != nil {
			return fmt.Errorf("rasterize %dx%d: %w", size, size, err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return fmt.Errorf("encode %dx%d png: %w", size, size, err)
		}
		encoded[size] = buf.Bytes()
	}

	for _, size := range pngSizes {
		out := filepath.Join(pngDir, fmt.Sprintf("icon_%d.png", size))
		if err := os.WriteFile(out, encoded[size], pngFilePerm); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
		fmt.Printf("wrote %s\n", out)
	}

	if err := os.WriteFile(embedPNG, encoded[embedSize], pngFilePerm); err != nil {
		return fmt.Errorf("write %s: %w", embedPNG, err)
	}
	fmt.Printf("wrote %s (%dx%d, embedded)\n", embedPNG, embedSize, embedSize)

	if err := os.WriteFile(icnsPath, buildICNS(encoded), pngFilePerm); err != nil {
		return fmt.Errorf("write %s: %w", icnsPath, err)
	}
	fmt.Printf("wrote %s\n", icnsPath)

	ico, err := buildICO(encoded)
	if err != nil {
		return fmt.Errorf("build ico: %w", err)
	}
	if err := os.WriteFile(icoPath, ico, pngFilePerm); err != nil {
		return fmt.Errorf("write %s: %w", icoPath, err)
	}
	fmt.Printf("wrote %s\n", icoPath)

	return nil
}

// allSizes is the de-duplicated union of every size any output needs.
func allSizes() []int {
	seen := map[int]bool{}
	var out []int
	add := func(n int) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range pngSizes {
		add(n)
	}
	for _, c := range icnsChunks {
		add(c.size)
	}
	for _, n := range icoSizes {
		add(n)
	}
	add(embedSize)
	return out
}

// rasterize renders the SVG natively at size x size pixels.
func rasterize(svg []byte, size int) (*image.NRGBA, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svg))
	if err != nil {
		return nil, fmt.Errorf("parse svg: %w", err)
	}
	icon.SetTarget(0, 0, float64(size), float64(size))

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scanner := rasterx.NewScannerGV(size, size, img, img.Bounds())
	raster := rasterx.NewDasher(size, size, scanner)
	icon.Draw(raster, 1.0)
	return img, nil
}

// buildICNS packs the PNG payloads into an Apple .icns container.
//
// Layout: the magic "icns", a big-endian uint32 holding the total file length,
// then one chunk per image: a 4-byte OSType, a big-endian uint32 chunk length
// that INCLUDES its own 8-byte header, then the raw PNG bytes.
func buildICNS(encoded map[int][]byte) []byte {
	var body bytes.Buffer
	for _, c := range icnsChunks {
		payload := encoded[c.size]
		body.WriteString(c.osType)
		_ = binary.Write(&body, binary.BigEndian, uint32(len(payload)+8))
		body.Write(payload)
	}

	var out bytes.Buffer
	out.WriteString("icns")
	_ = binary.Write(&out, binary.BigEndian, uint32(body.Len()+8))
	out.Write(body.Bytes())
	return out.Bytes()
}

// buildICO packs the PNG payloads into a Windows .ico container.
//
// Layout: a 6-byte ICONDIR (reserved=0, type=1, image count), then one 16-byte
// ICONDIRENTRY per image, then the PNG payloads concatenated. Everything is
// little-endian. Windows Vista and newer read PNG-compressed entries natively.
func buildICO(encoded map[int][]byte) ([]byte, error) {
	const dirEntrySize = 16
	offset := uint32(6 + dirEntrySize*len(icoSizes))

	var dir, payloads bytes.Buffer
	_ = binary.Write(&dir, binary.LittleEndian, uint16(0))             // reserved
	_ = binary.Write(&dir, binary.LittleEndian, uint16(1))             // type: icon
	_ = binary.Write(&dir, binary.LittleEndian, uint16(len(icoSizes))) // count

	for _, size := range icoSizes {
		if size > 256 {
			return nil, fmt.Errorf("ico entry %d exceeds the 256px format limit", size)
		}
		payload := encoded[size]

		// 256 is encoded as 0 in the single-byte width/height fields.
		dim := byte(size)
		if size == 256 {
			dim = 0
		}

		dir.WriteByte(dim)                                                // width
		dir.WriteByte(dim)                                                // height
		dir.WriteByte(0)                                                  // color count (0 = truecolor)
		dir.WriteByte(0)                                                  // reserved
		_ = binary.Write(&dir, binary.LittleEndian, uint16(1))            // color planes
		_ = binary.Write(&dir, binary.LittleEndian, uint16(32))           // bits per pixel
		_ = binary.Write(&dir, binary.LittleEndian, uint32(len(payload))) // bytes in resource
		_ = binary.Write(&dir, binary.LittleEndian, offset)               // offset of payload

		payloads.Write(payload)
		offset += uint32(len(payload))
	}

	out := make([]byte, 0, dir.Len()+payloads.Len())
	out = append(out, dir.Bytes()...)
	out = append(out, payloads.Bytes()...)
	return out, nil
}
