# Windows resources

`winres.json` describes what goes into `tppdesktop.exe` beyond code: the
application icon (`desktop/packaging/windows/app.ico`, which Explorer, the
taskbar and the Start-menu shortcut show) and the version block.

`go build` links the `rsrc_windows_*.syso` files next to `main.go` into any
Windows build of this package on its own; nothing else needs a flag. They are
generated, and checked in so the build needs no extra tool. After changing
`app.ico` or this file, regenerate them from `desktop/cmd/tppdesktop`:

    go run github.com/tc-hib/go-winres@latest make --arch amd64,arm64

The tray icon is not this one; it lives in `internal/tray`.

The version block in `winres.json` is a placeholder. CI regenerates the
`.syso` files with the build's real version before it links the release
binary (`.github/workflows/desktop.yml`, *Stamp the version into the Windows
resources*); the files committed here are never rewritten by it.
