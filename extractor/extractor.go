package extractor

import (
	"net/url"
	"path"
	"strings"
)

func ExtractURL(raw string) (string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}

	parsed.Fragment = ""

	cleanPath := strings.TrimSuffix(parsed.Path, "/")
	name := path.Base(cleanPath)

	return parsed.String(), name, nil
}
