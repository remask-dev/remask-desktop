package stream

import (
	"strings"
	"unicode"
)

const maxTokenLength = 96

type Resolver func(token string) (string, bool)

type Restorer struct {
	resolve Resolver
	pending map[string]string
}

func NewRestorer(resolve Resolver) *Restorer {
	return &Restorer{resolve: resolve, pending: make(map[string]string)}
}

func (r *Restorer) Feed(channel, delta string) string {
	value := r.pending[channel] + delta
	delete(r.pending, channel)

	var output strings.Builder
	for len(value) > 0 {
		start := strings.IndexByte(value, '<')
		if start < 0 {
			output.WriteString(value)
			break
		}
		output.WriteString(value[:start])
		value = value[start:]
		end := strings.IndexByte(value, '>')
		if end >= 0 {
			candidate := value[:end+1]
			if original, ok := r.resolve(candidate); ok {
				output.WriteString(original)
			} else {
				output.WriteString(candidate)
			}
			value = value[end+1:]
			continue
		}

		if isTokenPrefix(value) && len(value) <= maxTokenLength {
			r.pending[channel] = value
			break
		}
		output.WriteByte('<')
		value = value[1:]
	}
	return output.String()
}

func (r *Restorer) Flush(channel string) string {
	value := r.pending[channel]
	delete(r.pending, channel)
	return value
}

func (r *Restorer) FlushAll() map[string]string {
	result := r.pending
	r.pending = make(map[string]string)
	return result
}

func isTokenPrefix(value string) bool {
	if value == "<" {
		return true
	}
	if !strings.HasPrefix(value, "<") {
		return false
	}
	content := value[1:]
	colon := strings.IndexByte(content, ':')
	if colon < 0 {
		for index, char := range content {
			if index == 0 && !unicode.IsUpper(char) {
				return false
			}
			if !(unicode.IsUpper(char) || unicode.IsDigit(char) || char == '_') {
				return false
			}
		}
		return true
	}
	entityType, suffix := content[:colon], content[colon+1:]
	if entityType == "" || len(suffix) > 4 {
		return false
	}
	for index, char := range entityType {
		if index == 0 && !unicode.IsUpper(char) {
			return false
		}
		if !(unicode.IsUpper(char) || unicode.IsDigit(char) || char == '_') {
			return false
		}
	}
	for _, char := range suffix {
		if !strings.ContainsRune("0123456789ABCDEF", char) {
			return false
		}
	}
	return true
}
