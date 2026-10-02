package myserver

import (
	"fmt"
	"net/url"
	"strings"
)

// ParseOrigins splits a comma-separated list of browser origins
// ("https://app.example.com,https://*.example.org,http://localhost:*")
// and checks each one. "*" (any website) is refused: with credentials it
// would let every page a user visits read and write the root.
func ParseOrigins(list string) ([]string, error) {
	var out []string
	for _, o := range strings.Split(list, ",") {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" {
			continue
		}
		if o == "null" { // file:// pages and sandboxed iframes
			out = append(out, o)
			continue
		}
		if o == "*" {
			return nil, fmt.Errorf(`origin "*" would allow every website; list the sites instead`)
		}
		// wildcards are fine for rs/cors ("https://*.example.com", "http://localhost:*")
		u, err := url.Parse(strings.ReplaceAll(strings.ReplaceAll(o, ":*", ":1"), "*", "x"))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" {
			return nil, fmt.Errorf("invalid origin %q: want scheme://host[:port], e.g. https://app.example.com", o)
		}
		out = append(out, o)
	}
	return out, nil
}

// OriginList is a flag.Value collecting origins from repeated or
// comma-separated flags: -cors https://a.com -cors https://b.com,https://c.com
type OriginList []string

func (l *OriginList) String() string { return strings.Join(*l, ",") }

func (l *OriginList) Set(v string) error {
	o, err := ParseOrigins(v)
	if err != nil {
		return err
	}
	*l = append(*l, o...)
	return nil
}
