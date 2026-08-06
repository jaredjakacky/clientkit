package slogobserver

import (
	"log/slog"
	"strings"
	"time"

	"github.com/jaredjakacky/opskit"
)

func eventAttributes(attributes []opskit.Attribute) []slog.Attr {
	converted := make([]slog.Attr, 0, len(attributes))
	positions := make(map[string]int, len(attributes))
	for _, attribute := range attributes {
		if strings.TrimSpace(attribute.Key) == "" {
			continue
		}
		value := slog.String(attribute.Key, attribute.Value)
		if position, exists := positions[attribute.Key]; exists {
			converted[position] = value
			continue
		}
		positions[attribute.Key] = len(converted)
		converted = append(converted, value)
	}
	return converted
}

func addString(attributes *[]slog.Attr, key string, value string) {
	if value != "" {
		*attributes = append(*attributes, slog.String(key, value))
	}
}

func addDuration(attributes *[]slog.Attr, key string, value time.Duration) {
	if value >= 0 {
		*attributes = append(*attributes, slog.Duration(key, value))
	}
}
