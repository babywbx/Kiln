package config

func StripJSONC(in []byte) []byte {
	out := make([]byte, 0, len(in))
	i := 0
	n := len(in)
	inString := false
	escape := false
	for i < n {
		c := in[i]
		if inString {
			out = append(out, c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			i++
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			i++
			continue
		}
		if c == '/' && i+1 < n {
			if in[i+1] == '/' {
				i += 2
				for i < n && in[i] != '\n' {
					i++
				}
				continue
			}
			if in[i+1] == '*' {
				i += 2
				for i+1 < n && (in[i] != '*' || in[i+1] != '/') {
					i++
				}
				if i+1 < n {
					i += 2
				}
				continue
			}
		}
		out = append(out, c)
		i++
	}
	return stripTrailingCommas(out)
}

func stripTrailingCommas(in []byte) []byte {
	out := make([]byte, 0, len(in))
	i := 0
	n := len(in)
	inString := false
	escape := false
	for i < n {
		c := in[i]
		if inString {
			out = append(out, c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			i++
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			i++
			continue
		}
		if c == ',' {
			j := i + 1
			for j < n && (in[j] == ' ' || in[j] == '\t' || in[j] == '\n' || in[j] == '\r') {
				j++
			}
			if j < n && (in[j] == '}' || in[j] == ']') {
				i++
				continue
			}
		}
		out = append(out, c)
		i++
	}
	return out
}
