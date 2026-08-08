package mediaurl

import (
	"net/url"
)

func ResolveRef(baseURL, ref string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(reference).String(), nil
}
