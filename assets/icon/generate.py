#!/usr/bin/env python3
"""Generate every platform icon from assets/icon/icon.svg.

    pip install cairosvg pillow
    python3 assets/icon/generate.py

Re-run it after changing icon.svg and commit what it writes. The outputs are
checked in so no build needs Python. The one step this script leaves out is the
Windows resource object that puts app.ico into tppdesktop.exe, which Go tooling
builds (see desktop/cmd/tppdesktop/winres/README.md).

Tray icons are drawn separately (desktop/internal/tray) and this script does
not touch them.
"""

import io
import re
import xml.etree.ElementTree as ET
from pathlib import Path

import cairosvg
from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parents[2]
SRC = ROOT / "assets/icon/icon.svg"
SVG_NS = "{http://www.w3.org/2000/svg}"

# The artwork's bounding box in the SVG's 512-unit viewBox, after its group
# transform. Measured once from the paths. The circle and square crops below
# are centred on it.
ART_CENTER = (256.0, 255.5)
# How far the artwork reaches from ART_CENTER in any direction: the sync
# badge's far edge. Keeping this inside a circle is what keeps the icon clear of
# every launcher mask.
ART_RADIUS = 255.0

ANDROID_RES = ROOT / "mobile/android/app/src/main/res"
ANDROID_BG = "#FFFFFF"


def clean_svg() -> str:
    """The source SVG minus its embedded provenance manifest, which is several
    kilobytes that browsers would download with every favicon."""
    text = SRC.read_text()
    text = re.sub(r"<metadata>.*?</metadata>", "", text, flags=re.S)
    text = re.sub(r'\s+xmlns:c2pa="[^"]*"', "", text)
    return text


def render(size: int, scale: float = 1.0, background: str | None = None, shape: str = "none",
           corner: float = 0.0) -> Image.Image:
    """Render the artwork centred in a size×size canvas.

    scale is the artwork's radius as a fraction of the canvas's half-width;
    shape is the background plate: none, square (with corner radius as a
    fraction of the size) or circle.
    """
    # Render the SVG large, then place it: cairosvg has no notion of padding.
    art_px = round(size * scale * 512 / (2 * ART_RADIUS))
    png = cairosvg.svg2png(bytestring=clean_svg().encode(), output_width=art_px * 2,
                           output_height=art_px * 2)
    art = Image.open(io.BytesIO(png)).convert("RGBA").resize((art_px, art_px), Image.LANCZOS)

    canvas = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    if background:
        plate = Image.new("RGBA", (size, size), (0, 0, 0, 0))
        draw = ImageDraw.Draw(plate)
        if shape == "circle":
            draw.ellipse((0, 0, size - 1, size - 1), fill=background)
        elif shape == "square":
            draw.rounded_rectangle((0, 0, size - 1, size - 1), radius=round(size * corner), fill=background)
        canvas = plate

    offset_x = round(size / 2 - ART_CENTER[0] * art_px / 512)
    offset_y = round(size / 2 - ART_CENTER[1] * art_px / 512)
    canvas.alpha_composite(art, (offset_x, offset_y))
    return canvas


def save_png(img: Image.Image, path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    img.save(path, optimize=True)
    print("wrote", path.relative_to(ROOT))


def favicon_svg() -> str:
    """The SVG favicon. The artwork's navy all but disappears on a dark tab
    strip, so under a dark colour scheme it switches to a light ink; the
    coloured lines keep their own fills."""
    text = clean_svg()
    text = text.replace('<g fill="#2f3b59"', '<g class="ink" fill="#2f3b59"', 1)
    style = ("\n  <style>@media (prefers-color-scheme: dark) { .ink { fill: #e6ebf5; } }</style>")
    return re.sub(r"(<svg[^>]*>)", lambda m: m.group(1) + style, text, count=1)


# ICO has no dark variant, and Windows shows the exe's icon on dark and light
# backgrounds alike, so every ICO sits on a light rounded plate.
ICO_PLATE = "#FFFFFF"


def save_ico(path: Path, sizes: list[int], scale: float) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    frames = [render(s, scale, ICO_PLATE, "square", corner=0.2) for s in sizes]
    frames[-1].save(path, format="ICO", sizes=[(s, s) for s in sizes], append_images=frames[:-1])
    print("wrote", path.relative_to(ROOT))


def svg_paths():
    """The SVG's paths as (d, fill, even_odd), and its group's translate."""
    tree = ET.fromstring(SRC.read_text())
    group = tree.find(f"{SVG_NS}g")
    tx, ty = map(float, re.match(r"translate\(([-\d.]+)[ ,]+([-\d.]+)\)", group.get("transform")).groups())
    paths = []
    for p in group.findall(f"{SVG_NS}path"):
        d = " ".join(p.get("d").split())
        fill = p.get("fill", group.get("fill"))
        even_odd = p.get("fill-rule", group.get("fill-rule")) == "evenodd"
        paths.append((d, fill, even_odd))
    return paths, tx, ty


def android_vector(path: Path, size_dp: int, art_radius_dp: float, header: str, *,
                   mono: bool = False, tint: bool = False) -> None:
    """Write a VectorDrawable of the artwork: the SVG's own path data, placed by
    choosing a viewport that puts ART_RADIUS at art_radius_dp."""
    items, tx, ty = svg_paths()
    viewport = round(size_dp * ART_RADIUS / art_radius_dp, 2)
    gx = round(viewport / 2 - ART_CENTER[0] + tx, 2)
    gy = round(viewport / 2 - ART_CENTER[1] + ty, 2)

    lines = [header, '<vector xmlns:android="http://schemas.android.com/apk/res/android"',
             f'    android:width="{size_dp}dp"', f'    android:height="{size_dp}dp"',
             f'    android:viewportWidth="{viewport}"', f'    android:viewportHeight="{viewport}"']
    if tint:
        lines.append('    android:tint="?android:attr/colorControlNormal"')
    lines[-1] += ">"
    lines.append(f'    <group android:translateX="{gx}" android:translateY="{gy}">')
    for d, fill, even_odd in items:
        colour = "@android:color/white" if mono else fill.upper()
        lines.append("        <path")
        lines.append(f'            android:fillColor="{colour}"')
        if even_odd:
            lines.append('            android:fillType="evenOdd"')
        lines.append(f'            android:pathData="{d}" />')
    lines.append("    </group>")
    lines.append("</vector>")
    path.write_text("\n".join(lines) + "\n")
    print("wrote", path.relative_to(ROOT))


def main() -> None:
    svg = favicon_svg()

    # --- Master rasters, for stores and anything else that wants one.
    save_png(render(1024, 0.92), ROOT / "assets/icon/icon-1024.png")
    save_png(render(512, 0.66 * 0.97, ANDROID_BG, "square"), ROOT / "assets/icon/play-store-512.png")

    # --- Android launcher (SPEC §7.1). API 26+ uses the adaptive icon: a
    # 108dp foreground whose visible area is the central 72dp, of which only a
    # 66dp circle is guaranteed to survive every mask. The artwork is kept to
    # a 32dp radius inside that.
    android_vector(ANDROID_RES / "drawable/ic_launcher_foreground.xml", 108, 32,
                   "<!-- Generated by assets/icon/generate.py from assets/icon/icon.svg. -->")
    # Android 13 themed icons: the same shapes in one colour, which the system
    # tints to the wallpaper.
    android_vector(ANDROID_RES / "drawable/ic_launcher_monochrome.xml", 108, 32,
                   "<!-- Generated by assets/icon/generate.py: the themed-icon layer. -->", mono=True)
    # The quick-settings tile: a 24dp silhouette. The system draws only the
    # alpha channel, so every shape is white and the colour comes from the tint.
    android_vector(ANDROID_RES / "drawable/ic_tile.xml", 24, 11.2,
                   "<!-- Generated by assets/icon/generate.py: the quick-settings tile icon. -->",
                   mono=True, tint=True)

    # API 24–25 predates adaptive icons and needs bitmaps per density.
    for density, px in {"mdpi": 48, "hdpi": 72, "xhdpi": 96, "xxhdpi": 144, "xxxhdpi": 192}.items():
        save_png(render(px, 0.78, ANDROID_BG, "square", corner=0.18), ANDROID_RES / f"mipmap-{density}/ic_launcher.png")
        save_png(render(px, 0.72, ANDROID_BG, "circle"), ANDROID_RES / f"mipmap-{density}/ic_launcher_round.png")

    # --- Desktop (SPEC §7.2).
    icns = ROOT / "desktop/packaging/macos/AppIcon.icns"
    render(1024, 0.92).save(icns, format="ICNS")
    print("wrote", icns.relative_to(ROOT))
    save_ico(ROOT / "desktop/packaging/windows/app.ico", [16, 20, 24, 32, 40, 48, 64, 256], 0.84)

    # --- Web favicons: the desktop settings UI and the relay's two pages.
    for web in (ROOT / "desktop/ui/public", ROOT / "server/web/admin", ROOT / "server/web/pair"):
        web.mkdir(parents=True, exist_ok=True)
        (web / "favicon.svg").write_text(svg)
        print("wrote", (web / "favicon.svg").relative_to(ROOT))
        save_ico(web / "favicon.ico", [16, 32, 48], 0.86)
        save_png(render(180, 0.80, "#FFFFFF", "square"), web / "apple-touch-icon.png")


if __name__ == "__main__":
    main()
