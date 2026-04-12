package app

import (
	"fmt"
	"regexp"
	"strings"
)

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// maxSlugWords limits the number of dash-separated words in a generated
// slug. Keeps auto-generated slugs typeable. Existing slugs in the DB
// are never modified — this only affects new ones.
const maxSlugWords = 6

// Slugify converts a free-form name into a URL-safe slug.
// It lowercases, replaces runs of non-alphanumeric chars with dashes,
// strips leading/trailing dashes, and truncates to maxSlugWords words.
// Returns an error if the result is empty.
func Slugify(name string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "", fmt.Errorf("cannot slugify %q: result is empty", name)
	}
	// Truncate to maxSlugWords dash-separated parts.
	parts := strings.SplitN(s, "-", maxSlugWords+1)
	if len(parts) > maxSlugWords {
		s = strings.Join(parts[:maxSlugWords], "-")
	}
	return s, nil
}
