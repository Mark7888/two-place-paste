# App icon

`icon.svg` is the master. Every platform icon is generated from it by
`generate.py` and checked in:

| Output | Used by |
| --- | --- |
| `mobile/android/app/src/main/res/drawable/ic_launcher_foreground.xml`, `ic_launcher_monochrome.xml` | Android adaptive launcher icon (API 26+) and themed icon (Android 13+) |
| `mobile/android/app/src/main/res/mipmap-*/ic_launcher*.png` | Android launcher icon on API 24–25 |
| `mobile/android/app/src/main/res/drawable/ic_tile.xml` | Android quick-settings tile |
| `desktop/packaging/macos/AppIcon.icns` | macOS app bundle |
| `desktop/packaging/windows/app.ico` | Windows exe (via `desktop/cmd/tppdesktop/winres`) and installer |
| `desktop/ui/public/favicon.*`, `apple-touch-icon.png` | Desktop settings UI |
| `server/web/{admin,pair}/favicon.*`, `apple-touch-icon.png` | Relay admin UI and pairing hand-off page |
| `icon-outlined.svg` | The outlined variant the desktop icons and favicons are made from |
| `icon-1024.png`, `play-store-512.png` | Store listings and anything else that wants a bitmap |

To change the icon, edit `icon.svg`, then:

    pip install cairosvg pillow
    python3 assets/icon/generate.py
    cd desktop/cmd/tppdesktop && go run github.com/tc-hib/go-winres@latest make --arch amd64,arm64

Everything that can end up on a dark background is made to survive it. The
Android icons sit on white. The desktop icons (`.ico`, `.icns`) and the
favicons use `icon-outlined.svg`: the same artwork on a white sticker that
follows its silhouette. `generate.py` writes that file too; edit `icon.svg`,
not it.

The tray icons (`desktop/internal/tray`) are drawn separately and are not
generated from this file.
