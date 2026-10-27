package tray

// watchReopen does nothing on Windows: opening the app again starts a second
// process, which asks this one to open the app over the localhost server
// (localui.ShowRunning) instead.
func watchReopen(func()) {}
