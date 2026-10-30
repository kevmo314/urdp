package capture

// Temporary: only the primary display until multi-monitor capture lands.

func listDisplays() ([]Display, error) {
	src, err := openDesktop()
	if err != nil {
		return nil, err
	}
	defer src.Close()
	b := src.Bounds()
	return []Display{{Index: 1, Name: "primary", X: b.Min.X, Y: b.Min.Y, Width: b.Dx(), Height: b.Dy(), Primary: true}}, nil
}

func openDisplay(i int) (Source, error) {
	if i != 1 {
		return nil, ErrNotFound
	}
	return openDesktop()
}
