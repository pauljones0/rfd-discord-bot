package main

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

func cleanSoldQueryTitle(title string) string {
	title = html.UnescapeString(title)
	title = regexp.MustCompile(`(?i)\b(refurbished|excellent|good|fair|open box|brand new|renewed|windows|win\s*1[01]\s*pro|warranty|unlocked|wifi|bluetooth|generation|gen)\b`).ReplaceAllString(title, " ")
	title = regexp.MustCompile(`[^\pL\pN]+`).ReplaceAllString(title, " ")
	words := strings.Fields(title)
	if len(words) > 8 {
		words = words[:8]
	}
	return strings.Join(words, " ")
}

func main() {
	titles := []string{
		"The hp omnidesk desktop pc ryzen 7 8700g/16gb/1tb ssd",
		"The refurb macbook air m5/16gb ram/ 1tb ssd",
		"The lenovo yoga mouse",
		"The refurb seagate 10tb hdd",
		"The oneplus nord n30",
		"The ipad air m3 128gb",
		"The apple ipad pro 13\" 256gb",
		"The samsung galaxy ring black titanium size 11",
		"The samsung galaxy tab a9+ 11\" 64gb",
		// Add some extra nasty best buy examples
		"Apple iPad Air (M3) 11\" 128GB WiFi Bluetooth Unlocked",
		"OnePlus Nord N30 5G 128GB - Chromatic Gray - Refurbished",
		"HP Omnidesk Desktop PC Ryzen 7 8700G/16GB/1TB SSD Windows 11 Pro WiFi Bluetooth - Refurbished",
	}

	for _, t := range titles {
		fmt.Printf("Original: %s\nCleaned:  %s\n\n", t, cleanSoldQueryTitle(t))
	}
}
