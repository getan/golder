package computeruse

// The Kitty placement covers width columns and height-4 rows starting below
// the header. Terminal mouse coordinates are 1-based; aim at the cell center.
// Header/footer clicks, stale views and Agent-owned views never dispatch.
func panePreviewPoint(state paneViewerState, click paneMouseClick, width, height int) (x, y float64, ok bool) {
	if !state.status.OperatorControl || !state.status.Paused || state.localPaused || state.semantic || !state.graphics || state.frame == nil || state.frame.ID == "" || width < 1 || height <= 4 ||
		click.column < 1 || click.column > width || click.row < 2 || click.row >= height-2 {
		return 0, 0, false
	}
	x = (float64(click.column) - 0.5) / float64(width) * float64(state.frame.Width)
	y = (float64(click.row) - 1.5) / float64(height-4) * float64(state.frame.Height)
	return x, y, true
}
