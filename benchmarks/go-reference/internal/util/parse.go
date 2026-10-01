package util

import (
	"regexp"
	"strconv"
	"strings"
)

func SafeAtoi(s string) int {
	i, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return i
}

var nonNumericRegex = regexp.MustCompile(`[^\d]`)

func CleanNumericString(s string) string {
	return nonNumericRegex.ReplaceAllString(s, "")
}

var extractSignedNumberRegex = regexp.MustCompile(`-?\d+`)

func ParseSignedNumericString(s string) string {
	return extractSignedNumberRegex.FindString(s)
}
