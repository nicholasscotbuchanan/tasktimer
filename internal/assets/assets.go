// Package assets holds the static resources compiled into the binaries.
//
// icon.png is generated from icon.svg — the single source of truth — by
// `go run ./tools/icongen`, which also emits the .icns and .ico the packaging
// scripts need. It is committed so that a plain `go build` works without
// running the generator first.
//
// The fonts are Inter 3.19, the hinted desktop TTFs from
// https://github.com/rsms/inter, licensed under the SIL Open Font License 1.1.
// The licence travels with them in fonts/OFL.txt, as the OFL requires.
//
// They are vendored rather than taken from Fyne because Fyne bundles only
// Inter-Regular — and only as the source for its symbol font. Its actual text
// font is NotoSans, so the app needs its own copy to render in Inter at every
// weight. The Regular here is byte-identical to Fyne's, which is what
// guarantees the four styles are a matched family rather than a mix of
// releases.
package assets

import _ "embed"

// AppIconPNG is the 512x512 application icon. PNG rather than the source SVG
// because the system tray rasterises it on platforms that will not take vector
// input.
//
//go:embed icon.png
var AppIconPNG []byte

// The Inter faces backing the app's theme. Fyne asks for a face per TextStyle,
// so all four combinations of bold and italic are carried even though nothing
// currently renders italic: falling back to NotoSans for the styles Inter did
// not cover would put two typefaces on one screen the moment anything did.
var (
	//go:embed fonts/Inter-Regular.ttf
	InterRegular []byte

	//go:embed fonts/Inter-Bold.ttf
	InterBold []byte

	//go:embed fonts/Inter-Italic.ttf
	InterItalic []byte

	//go:embed fonts/Inter-BoldItalic.ttf
	InterBoldItalic []byte
)
