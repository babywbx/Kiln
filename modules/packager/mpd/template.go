package mpd

import (
	"fmt"
	"strconv"
	"strings"
)

func expandStatic(tmpl, repID string, bandwidth int) string {
	return expandWith(tmpl, func(name, format string) (string, bool) {
		switch name {
		case "RepresentationID":
			return repID, true
		case "Bandwidth":
			return formatIdentifier(uint64(bandwidth), format), true
		default:
			return "", false
		}
	})
}

func expandIdentifiers(tmpl, repID string, bandwidth int, number, time uint64) string {
	return expandWith(tmpl, func(name, format string) (string, bool) {
		switch name {
		case "RepresentationID":
			return repID, true
		case "Bandwidth":
			return formatIdentifier(uint64(bandwidth), format), true
		case "Number":
			return formatIdentifier(number, format), true
		case "Time":
			return formatIdentifier(time, format), true
		default:
			return "", false
		}
	})
}

func expandWith(tmpl string, lookup func(name, format string) (string, bool)) string {
	var b strings.Builder
	for i := 0; i < len(tmpl); {
		c := tmpl[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(tmpl[i+1:], '$')
		if end < 0 {
			b.WriteString(tmpl[i:])
			break
		}
		body := tmpl[i+1 : i+1+end]
		i += end + 2
		if body == "" {
			b.WriteByte('$')
			continue
		}
		name, format, _ := strings.Cut(body, "%")
		val, ok := lookup(name, format)
		if !ok {
			b.WriteByte('$')
			b.WriteString(body)
			b.WriteByte('$')
			continue
		}
		b.WriteString(val)
	}
	return b.String()
}

func formatIdentifier(v uint64, format string) string {
	if format == "" {
		return strconv.FormatUint(v, 10)
	}
	if !strings.HasSuffix(format, "d") {
		return strconv.FormatUint(v, 10)
	}
	return fmt.Sprintf("%"+format, v)
}
