package update

import (
	"fmt"
	"runtime"
)

// AssetName is the file this machine installs: the names the desktop workflow
// gives its outputs (TwoPlacePaste-<platform>.zip / .exe).
//
// An Intel build running under Rosetta on Apple Silicon asks for the arm64
// build, so its next update moves it onto the native one.
func AssetName() (string, error) {
	return assetName(runtime.GOOS, runtime.GOARCH, translated())
}

func assetName(goos, goarch string, rosetta bool) (string, error) {
	switch {
	case goos == "darwin" && (goarch == "arm64" || rosetta):
		return "TwoPlacePaste-macOS-arm64.zip", nil
	case goos == "darwin" && goarch == "amd64":
		return "TwoPlacePaste-macOS-x64.zip", nil
	case goos == "windows" && goarch == "amd64":
		return "TwoPlacePaste-Windows-x64.exe", nil
	}
	return "", fmt.Errorf("update: no builds are published for %s/%s", goos, goarch)
}
