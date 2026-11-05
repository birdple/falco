package utils

// IsValidImageID validates that an image ID contains only safe characters
func IsValidImageID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}

	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_':
		default:
			return false
		}
	}

	return true
}
