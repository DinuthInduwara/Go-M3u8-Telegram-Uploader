package extractor

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

func GrabM3u8URL(url string) (string, error) {
	m3u8URL, err := GetM3u8URL(url)
	if err != nil {
		return "", fmt.Errorf("program cannot find any m3u8 file (exit code %d)", err)
	}

	if m3u8URL == "" {
		return "", errors.New("no playlist.m3u8 URL found in output file")
	}

	return m3u8URL, nil
}

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
