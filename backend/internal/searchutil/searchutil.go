package searchutil

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var normalizeReplacer = strings.NewReplacer(
	"-", " ",
	".", " ",
	"_", " ",
	",", " ",
	":", " ",
	";", " ",
	"!", " ",
	"?", " ",
	"(", " ",
	")", " ",
	"[", " ",
	"]", " ",
	"{", " ",
	"}", " ",
	"'", " ",
	"\"", " ",
	"/", " ",
	"\\", " ",
	"|", " ",
	"+", " ",
	"=", " ",
	"#", " ",
	"&", " ",
	"*", " ",
)

var (
	relatedLabelLinePattern = regexp.MustCompile(`(?i)^(?:alternative(?:\s+(?:titles?|names?))?|associated\s+names?|other\s+names?|aliases?|synonyms?)\s*[:\-]\s*(.+)$`)
	relatedLabelOnlyPattern = regexp.MustCompile(`(?i)^(?:alternative(?:\s+(?:titles?|names?))?|associated\s+names?|other\s+names?|aliases?|synonyms?)\s*[:\-]?\s*$`)

	relatedJSONArrayPattern  = regexp.MustCompile(`(?is)"(?:alternativeTitles|alternative_titles|alternativeNames|alternative_names|altTitles|alt_titles|otherTitles|other_titles|aliases|synonyms)"\s*:\s*\[(.*?)\]`)
	relatedJSONStringPattern = regexp.MustCompile(`(?is)"(?:alternativeTitles|alternative_titles|alternativeNames|alternative_names|altTitles|alt_titles|otherTitles|other_titles|aliases|synonyms)"\s*:\s*"([^"]+)"`)
	jsonQuotedStringPattern  = regexp.MustCompile(`"([^"\\]*(?:\\.[^"\\]*)*)"`)

	relatedBlockDelimiterReplacer = strings.NewReplacer(
		"|", "\n",
		";", "\n",
		"•", "\n",
		" / ", "\n",
		",", "\n",
	)

	htmlLineBreakReplacer = strings.NewReplacer(
		"<br>", "\n",
		"<br/>", "\n",
		"<br />", "\n",
		"</p>", "\n",
		"</div>", "\n",
		"</li>", "\n",
		"</dd>", "\n",
		"</td>", "\n",
		"</tr>", "\n",
	)

	htmlTagStripPattern      = regexp.MustCompile(`(?is)<[^>]+>`)
	htmlWhitespaceLinePatter = regexp.MustCompile(`[ \t]+`)
)

func Normalize(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	if clean == "" {
		return ""
	}
	clean = normalizeReplacer.Replace(clean)
	return strings.Join(strings.Fields(clean), " ")
}

func TokenizeNormalized(normalized string) []string {
	trimmed := strings.TrimSpace(normalized)
	if trimmed == "" {
		return nil
	}

	parts := strings.Fields(trimmed)
	tokens := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		tokens = append(tokens, part)
	}

	return tokens
}

func MatchesQuery(candidate string, normalizedQuery string, queryTokens []string) bool {
	normalizedCandidate := Normalize(candidate)
	if normalizedCandidate == "" {
		return false
	}

	if normalizedQuery != "" && strings.Contains(normalizedCandidate, normalizedQuery) {
		return true
	}
	if len(queryTokens) == 0 {
		return false
	}

	for _, token := range queryTokens {
		if token == "" {
			continue
		}
		if !strings.Contains(normalizedCandidate, token) {
			return false
		}
	}

	return true
}

func AnyCandidateMatches(candidates []string, normalizedQuery string, queryTokens []string) bool {
	for _, candidate := range candidates {
		if MatchesQuery(candidate, normalizedQuery, queryTokens) {
			return true
		}
	}
	return false
}

func UniqueNonEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	unique := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		key := Normalize(trimmed)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, trimmed)
	}

	return unique
}

func FilterEnglishAlphabetNames(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	filtered := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || !IsEnglishAlphabetName(trimmed) {
			continue
		}

		key := Normalize(trimmed)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}

		seen[key] = struct{}{}
		filtered = append(filtered, trimmed)
	}

	return filtered
}

func IsEnglishAlphabetName(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}

	hasLetter := false
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
			hasLetter = true
		case r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
		case unicode.IsSpace(r):
		case isAllowedASCIISeparator(r):
		default:
			return false
		}
	}

	return hasLetter
}

func isAllowedASCIISeparator(r rune) bool {
	switch r {
	case '-', '\'', '&', ':', ';', ',', '.', '!', '?', '(', ')', '[', ']', '{', '}', '/', '\\', '+':
		return true
	default:
		return false
	}
}

func ExtractRelatedTitles(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	candidates := make([]string, 0, 12)
	candidates = append(candidates, extractJSONRelatedTitles(trimmed)...)
	candidates = append(candidates, extractTextRelatedTitles(trimmed)...)

	return FilterEnglishAlphabetNames(candidates)
}

func extractJSONRelatedTitles(raw string) []string {
	titles := make([]string, 0, 8)

	for _, match := range relatedJSONArrayPattern.FindAllStringSubmatch(raw, -1) {
		if len(match) < 2 {
			continue
		}
		for _, stringMatch := range jsonQuotedStringPattern.FindAllStringSubmatch(match[1], -1) {
			if len(stringMatch) < 2 {
				continue
			}
			value := decodeJSONString(stringMatch[1])
			if value == "" {
				continue
			}
			titles = append(titles, splitRelatedTitleBlock(value)...)
		}
	}

	for _, match := range relatedJSONStringPattern.FindAllStringSubmatch(raw, -1) {
		if len(match) < 2 {
			continue
		}
		value := decodeJSONString(match[1])
		if value == "" {
			continue
		}
		titles = append(titles, splitRelatedTitleBlock(value)...)
	}

	return UniqueNonEmpty(titles)
}

func extractTextRelatedTitles(raw string) []string {
	normalized := htmlLineBreakReplacer.Replace(raw)
	normalized = htmlTagStripPattern.ReplaceAllString(normalized, " ")
	normalized = html.UnescapeString(normalized)
	normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	lines := strings.Split(normalized, "\n")
	collected := make([]string, 0, 8)
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(htmlWhitespaceLinePatter.ReplaceAllString(lines[index], " "))
		if line == "" {
			continue
		}

		labelMatch := relatedLabelLinePattern.FindStringSubmatch(line)
		if len(labelMatch) >= 2 {
			collected = append(collected, splitRelatedTitleBlock(labelMatch[1])...)
			continue
		}

		if relatedLabelOnlyPattern.MatchString(line) {
			for next := index + 1; next < len(lines); next++ {
				nextLine := strings.TrimSpace(htmlWhitespaceLinePatter.ReplaceAllString(lines[next], " "))
				if nextLine == "" {
					continue
				}
				collected = append(collected, splitRelatedTitleBlock(nextLine)...)
				break
			}
		}
	}

	return UniqueNonEmpty(collected)
}

func splitRelatedTitleBlock(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	normalized := relatedBlockDelimiterReplacer.Replace(trimmed)
	parts := strings.Split(normalized, "\n")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		candidate := strings.TrimSpace(part)
		if candidate == "" {
			continue
		}
		result = append(result, candidate)
	}

	return result
}

func decodeJSONString(raw string) string {
	if raw == "" {
		return ""
	}
	unquoted, err := strconv.Unquote(`"` + raw + `"`)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return strings.TrimSpace(unquoted)
}
