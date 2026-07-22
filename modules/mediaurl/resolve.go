package mediaurl

import (
	"net/url"
	"path"
	"strings"
)

func ResolveRef(baseURL, ref string) (string, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(ref, "/") {
		u.Path = ref
		u.RawQuery = ""
		u.Fragment = ""
		return u.String(), nil
	}
	directory := path.Dir(u.Path)
	if directory == "." {
		directory = "/"
	}
	u.Path = path.Join(directory, ref)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
