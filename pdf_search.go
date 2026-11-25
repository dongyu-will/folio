package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

type pdfSearchHit struct {
	Page    int    `json:"page"`
	Snippet string `json:"snippet"`
}

func searchPDFText(docPath, query string, maxHits int) ([]pdfSearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" || docPath == "" {
		return nil, nil
	}
	if maxHits <= 0 {
		maxHits = 200
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	hits, ok := searchPDFPlatform(ctx, docPath, query, maxHits)
	cancel()
	if ok {
		return hits, nil
	}
	return searchPDFTextPureGo(docPath, query, maxHits)
}

var (
	pdfPageMarkerRe = regexp.MustCompile(`/Type\s*/Page\b`)
	pdfStreamRe     = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\n?endstream`)
)

// searchPDFTextPureGo scans raw and FlateDecode PDF content streams for text
// literals and hex strings as a zero-CGO fallback when native PDFKit is unavailable.
func searchPDFTextPureGo(docPath, query string, maxHits int) ([]pdfSearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if maxHits <= 0 {
		maxHits = 200
	}
	raw, err := os.ReadFile(docPath)
	if err != nil {
		return nil, err
	}

	pageStarts := pdfPageMarkerRe.FindAllIndex(raw, -1)
	matches := pdfStreamRe.FindAllSubmatchIndex(raw, -1)
	if len(matches) == 0 {
		// Fall back to direct literal extraction if no stream wrapper exists.
		text := extractPDFTextStrings(raw)
		return findQueryHitsInText(0, text, query, maxHits), nil
	}

	var hits []pdfSearchHit
	queryLower := strings.ToLower(query)
	for idx, loc := range matches {
		if len(hits) >= maxHits {
			break
		}
		streamBytes := raw[loc[2]:loc[3]]
		decoded := inflatePDFStreamIfZlib(streamBytes)
		extracted := extractPDFTextStrings(decoded)
		if !strings.Contains(strings.ToLower(extracted), queryLower) {
			continue
		}
		page := inferPageForOffset(loc[0], idx, pageStarts)
		pageHits := findQueryHitsInText(page, extracted, query, maxHits-len(hits))
		hits = append(hits, pageHits...)
	}
	return hits, nil
}

func inferPageForOffset(offset, streamIndex int, pageStarts [][]int) int {
	if len(pageStarts) == 0 {
		return streamIndex
	}
	best := 0
	for i, loc := range pageStarts {
		if loc[0] <= offset {
			best = i
		} else {
			break
		}
	}
	return best
}

func inflatePDFStreamIfZlib(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 {
		return data
	}
	zr, err := zlib.NewReader(bytes.NewReader(trimmed))
	if err != nil {
		return data
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, 8<<20))
	if err != nil || len(out) == 0 {
		return data
	}
	return out
}

func extractPDFTextStrings(stream []byte) string {
	var b strings.Builder
	i := 0
	for i < len(stream) {
		ch := stream[i]
		if ch == '(' {
			val, next := readPDFLiteral(stream, i+1)
			if val != "" {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(val)
			}
			i = next
			continue
		}
		if ch == '<' && i+1 < len(stream) && stream[i+1] != '<' {
			end := bytes.IndexByte(stream[i+1:], '>')
			if end >= 0 {
				if val := decodePDFHexString(stream[i+1 : i+1+end]); val != "" {
					if b.Len() > 0 {
						b.WriteByte(' ')
					}
					b.WriteString(val)
				}
				i += end + 2
				continue
			}
		}
		i++
	}
	return b.String()
}

func readPDFLiteral(data []byte, start int) (string, int) {
	var buf []byte
	depth := 1
	i := start
	for i < len(data) && depth > 0 {
		ch := data[i]
		switch ch {
		case '\\':
			i++
			if i >= len(data) {
				break
			}
			esc := data[i]
			switch esc {
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			case '(', ')', '\\':
				buf = append(buf, esc)
			case '\r', '\n':
				// Line continuation.
			default:
				if esc >= '0' && esc <= '7' {
					val := int(esc - '0')
					for k := 0; k < 2 && i+1 < len(data) && data[i+1] >= '0' && data[i+1] <= '7'; k++ {
						i++
						val = val*8 + int(data[i]-'0')
					}
					buf = append(buf, byte(val))
				} else {
					buf = append(buf, esc)
				}
			}
		case '(':
			depth++
			buf = append(buf, ch)
		case ')':
			depth--
			if depth > 0 {
				buf = append(buf, ch)
			}
		default:
			buf = append(buf, ch)
		}
		i++
	}
	return decodePDFStringBytes(buf), i
}

func decodePDFHexString(hexBytes []byte) string {
	var clean []byte
	for _, c := range hexBytes {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	if len(clean)%2 == 1 {
		clean = append(clean, '0')
	}
	raw := make([]byte, len(clean)/2)
	if _, err := hex.Decode(raw, clean); err != nil {
		return ""
	}
	return decodePDFStringBytes(raw)
}

func decodePDFStringBytes(raw []byte) string {
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		u16 := make([]uint16, 0, (len(raw)-2)/2)
		for i := 2; i+1 < len(raw); i += 2 {
			u16 = append(u16, uint16(raw[i])<<8|uint16(raw[i+1]))
		}
		return string(utf16.Decode(u16))
	}
	return string(raw)
}

func findQueryHitsInText(page int, text, query string, maxHits int) []pdfSearchHit {
	if maxHits <= 0 {
		return nil
	}
	runes := []rune(text)
	lowerRunes := []rune(strings.ToLower(text))
	qRunes := []rune(strings.ToLower(query))
	if len(qRunes) == 0 || len(lowerRunes) < len(qRunes) {
		return nil
	}
	var hits []pdfSearchHit
	for i := 0; i <= len(lowerRunes)-len(qRunes); i++ {
		matched := true
		for j := range qRunes {
			if lowerRunes[i+j] != qRunes[j] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		start := max(0, i-24)
		end := min(len(runes), i+len(qRunes)+40)
		snippet := strings.TrimSpace(strings.Join(strings.Fields(string(runes[start:end])), " "))
		if start > 0 {
			snippet = "…" + snippet
		}
		if end < len(runes) {
			snippet += "…"
		}
		hits = append(hits, pdfSearchHit{Page: page, Snippet: snippet})
		if len(hits) >= maxHits {
			break
		}
		i += len(qRunes) - 1
	}
	return hits
}
