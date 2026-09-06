package cache

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Query options are excluded so a URL cannot override the fixed timeout,
// retry, protocol or pool settings. rediss uses the client's verified TLS.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("invalid Redis URL")
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return errors.New("invalid Redis URL")
		}
	}
	if database := strings.TrimPrefix(u.Path, "/"); database != "" {
		number, err := strconv.ParseUint(database, 10, 31)
		if err != nil || number > 2147483647 {
			return errors.New("invalid Redis URL")
		}
	}
	return nil
}
