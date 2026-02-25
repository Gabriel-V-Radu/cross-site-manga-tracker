package repository

import "strings"

func sqlPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	if count == 1 {
		return "?"
	}

	var builder strings.Builder
	builder.Grow((count * 2) - 1)
	for index := 0; index < count; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteByte('?')
	}

	return builder.String()
}
