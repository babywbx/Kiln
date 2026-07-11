//go:build ignore

package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	unihanURL = "https://www.unicode.org/Public/UCD/latest/ucd/Unihan.zip"
	base64Set = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	cjkFirst  = 0x4E00
	cjkLast   = 0x9FFF
)

func main() {
	source := flag.String("unihan", "", "path to a local Unihan.zip (downloaded when empty)")
	out := flag.String("out", "modules/httpserver/admin/assets/data/romanize.js", "generated module path")
	flag.Parse()

	raw, err := loadUnihan(*source)
	if err != nil {
		fail(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		fail(err)
	}

	readings, err := readMember(archive, "Unihan_Readings.txt")
	if err != nil {
		fail(err)
	}
	variants, err := readMember(archive, "Unihan_Variants.txt")
	if err != nil {
		fail(err)
	}

	pinyin := collectReadings(readings, []string{"kMandarin", "kXHC1983", "kTGHZ2013"}, stripTone)
	jyutping := collectReadings(readings, []string{"kCantonese"}, stripDigits)
	simplify := collectSimplified(variants)

	var body strings.Builder
	body.WriteString("export const PINYIN = ")
	body.WriteString(encodeBuckets(pinyin))
	body.WriteString(";\n\nexport const JYUTPING = ")
	body.WriteString(encodeBuckets(jyutping))
	body.WriteString(";\n\nexport const SIMPLIFY = ")
	body.WriteString(encodePairs(simplify))
	body.WriteString(";\n")

	if err := os.MkdirAll(dir(*out), 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, []byte(body.String()), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s\n  pinyin   %d syllables\n  jyutping %d syllables\n  simplify %d pairs\n  size     %.1f KiB\n",
		*out, len(pinyin), len(jyutping), len(simplify), float64(body.Len())/1024)
}

func loadUnihan(path string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	response, err := http.Get(unihanURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download Unihan: %s", response.Status)
	}
	return io.ReadAll(response.Body)
}

func readMember(archive *zip.Reader, name string) ([]string, error) {
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		handle, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer handle.Close()
		content, err := io.ReadAll(handle)
		if err != nil {
			return nil, err
		}
		return strings.Split(string(content), "\n"), nil
	}
	return nil, fmt.Errorf("%s missing from archive", name)
}

func collectReadings(lines []string, fields []string, clean func(string) string) map[string][]int {
	wanted := map[string]struct{}{}
	for _, field := range fields {
		wanted[field] = struct{}{}
	}
	buckets := map[string]map[int]struct{}{}
	for _, line := range lines {
		code, name, value, ok := parseLine(line)
		if _, use := wanted[name]; !ok || !use || code < cjkFirst || code > cjkLast {
			continue
		}
		for _, token := range strings.Fields(value) {
			if index := strings.LastIndex(token, ":"); index >= 0 {
				token = token[index+1:]
			}
			reading := clean(token)
			if reading == "" {
				continue
			}
			if buckets[reading] == nil {
				buckets[reading] = map[int]struct{}{}
			}
			buckets[reading][code] = struct{}{}
		}
	}
	result := make(map[string][]int, len(buckets))
	for reading, codes := range buckets {
		list := make([]int, 0, len(codes))
		for code := range codes {
			list = append(list, code)
		}
		sort.Ints(list)
		result[reading] = list
	}
	return result
}

func collectSimplified(lines []string) [][2]int {
	seen := map[int]int{}
	for _, line := range lines {
		code, name, value, ok := parseLine(line)
		if !ok || name != "kSimplifiedVariant" || code < cjkFirst || code > cjkLast {
			continue
		}
		for _, token := range strings.Fields(value) {
			target, err := parseCode(token)
			if err != nil || target == code || target < cjkFirst || target > cjkLast {
				continue
			}
			if _, exists := seen[code]; !exists {
				seen[code] = target
			}
			break
		}
	}
	pairs := make([][2]int, 0, len(seen))
	for from, to := range seen {
		pairs = append(pairs, [2]int{from, to})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	return pairs
}

func parseLine(line string) (int, string, string, bool) {
	if !strings.HasPrefix(line, "U+") {
		return 0, "", "", false
	}
	parts := strings.Split(strings.TrimRight(line, "\r"), "\t")
	if len(parts) < 3 {
		return 0, "", "", false
	}
	code, err := parseCode(parts[0])
	if err != nil {
		return 0, "", "", false
	}
	return code, parts[1], parts[2], true
}

func parseCode(token string) (int, error) {
	value, err := strconv.ParseInt(strings.TrimPrefix(token, "U+"), 16, 32)
	return int(value), err
}

func stripTone(reading string) string {
	var sb strings.Builder
	for _, r := range reading {
		switch {
		case r >= 'a' && r <= 'z':
			sb.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			sb.WriteRune(r + 32)
		default:
			if folded, ok := toneFolding[r]; ok {
				sb.WriteRune(folded)
			}
		}
	}
	return sb.String()
}

func stripDigits(reading string) string {
	var sb strings.Builder
	for _, r := range reading {
		if r >= 'a' && r <= 'z' {
			sb.WriteRune(r)
		} else if r >= 'A' && r <= 'Z' {
			sb.WriteRune(r + 32)
		}
	}
	return sb.String()
}

var toneFolding = map[rune]rune{
	'ā': 'a', 'á': 'a', 'ǎ': 'a', 'à': 'a',
	'ē': 'e', 'é': 'e', 'ě': 'e', 'è': 'e', 'ê': 'e', 'ế': 'e', 'ề': 'e',
	'ī': 'i', 'í': 'i', 'ǐ': 'i', 'ì': 'i',
	'ō': 'o', 'ó': 'o', 'ǒ': 'o', 'ò': 'o',
	'ū': 'u', 'ú': 'u', 'ǔ': 'u', 'ù': 'u',
	'ü': 'v', 'ǖ': 'v', 'ǘ': 'v', 'ǚ': 'v', 'ǜ': 'v',
	'ń': 'n', 'ň': 'n', 'ǹ': 'n', 'ḿ': 'm',
}

func vlq(value int) string {
	var sb strings.Builder
	for {
		chunk := value & 31
		value >>= 5
		if value != 0 {
			chunk |= 32
		}
		sb.WriteByte(base64Set[chunk])
		if value == 0 {
			return sb.String()
		}
	}
}

func encodeBuckets(buckets map[string][]int) string {
	readings := make([]string, 0, len(buckets))
	for reading := range buckets {
		readings = append(readings, reading)
	}
	sort.Strings(readings)

	var sb strings.Builder
	sb.WriteString("{")
	for i, reading := range readings {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(strconv.Quote(reading))
		sb.WriteString(":\"")
		previous := 0
		for _, code := range buckets[reading] {
			sb.WriteString(vlq(code - previous))
			previous = code
		}
		sb.WriteString("\"")
	}
	sb.WriteString("}")
	return sb.String()
}

func encodePairs(pairs [][2]int) string {
	var from, to strings.Builder
	previous := 0
	for _, pair := range pairs {
		from.WriteString(vlq(pair[0] - previous))
		previous = pair[0]
		to.WriteString(vlq(pair[1] - cjkFirst))
	}
	return "[" + strconv.Quote(from.String()) + "," + strconv.Quote(to.String()) + "]"
}

func dir(path string) string {
	if index := strings.LastIndex(path, "/"); index > 0 {
		return path[:index]
	}
	return "."
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen-romanize-data:", err)
	os.Exit(1)
}
