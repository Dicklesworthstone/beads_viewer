package updater

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

var (
	errUpdaterRedirectLimit     = errors.New("too many redirects")
	errUpdaterRedirectAuthority = errors.New("updater redirects require HTTPS, a valid host, port 443, and no URL credentials")
)

// secureUpdaterURL permits authenticated HTTPS on its standard port. Redirect
// hosts are deliberately not restricted to a fixed CDN list: GitHub can change
// its download infrastructure, but crossing origins always removes our token.
func secureUpdaterURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Hostname() == "" {
		return false
	}
	if strings.HasSuffix(u.Host, ":") || u.Port() != "" && u.Port() != "443" {
		return false
	}
	// Validate the authority separately, including manually constructed URLs.
	// URL.Port reports an empty port for some malformed authorities.
	authority, err := url.Parse("https://" + u.Host)
	return err == nil && authority.Host == u.Host && authority.User == nil &&
		authority.Path == "" && authority.RawQuery == "" && authority.Fragment == ""
}

// githubCredentialOrigin recognizes only the two origins used for initial
// updater requests. Compare ASCII authorities, not Unicode-lowercased hostnames:
// Unicode case mapping and the HTTP transport's IDNA mapping can disagree.
func githubCredentialOrigin(u *url.URL) string {
	if !secureUpdaterURL(u) {
		return ""
	}
	for i := range len(u.Host) {
		if u.Host[i] >= 0x80 {
			return ""
		}
	}
	switch strings.ToLower(u.Host) {
	case "github.com", "github.com:443":
		return "https://github.com"
	case "api.github.com", "api.github.com:443":
		return "https://api.github.com"
	default:
		return ""
	}
}

func validateUpdaterRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errUpdaterRedirectLimit
	}
	if req == nil || !secureUpdaterURL(req.URL) {
		// Do not include the URL: a rejected redirect may contain credentials or
		// a signed CDN query. The request must never reach the transport.
		return errUpdaterRedirectAuthority
	}
	return nil
}

// updaterRedirectAuthorization never grants a new credential on a redirect.
// Every preceding request must carry the still-authorized original token and
// belong to the same origin. This also prevents net/http's original-header copy
// from restoring Authorization after a cross-origin hop returns to GitHub.
func updaterRedirectAuthorization(req *http.Request, via []*http.Request) string {
	if len(via) == 0 {
		return ""
	}
	origin := githubCredentialOrigin(req.URL)
	if origin == "" {
		return ""
	}
	token := githubToken()
	if token == "" {
		return ""
	}
	authorization := "Bearer " + token
	for _, previous := range via {
		if previous == nil || githubCredentialOrigin(previous.URL) != origin || previous.Header.Get("Authorization") != authorization {
			return ""
		}
	}
	return authorization
}

// doUpdaterRequest applies one redirect policy to metadata, checksum, and archive
// requests. Clone the client so injected clients and concurrent callers retain
// their settings; the transport, cookie jar, and timeout remain unchanged.
func doUpdaterRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	secured := *client
	checkRedirect := client.CheckRedirect
	secured.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if err := validateUpdaterRedirect(next, via); err != nil {
			return err
		}
		next.Header.Del("Authorization")
		if authorization := updaterRedirectAuthorization(next, via); authorization != "" {
			// net/http may strip auth on an ASCII DNS-case change. Reconstruct it
			// only after proving that every hop stayed within the original origin.
			next.Header.Set("Authorization", authorization)
		}
		if checkRedirect != nil {
			if err := checkRedirect(next, via); err != nil {
				return err
			}
			// Preserve stricter callback decisions, including removing auth, but
			// do not let a callback mutation widen this request's authority.
			if err := validateUpdaterRedirect(next, via); err != nil {
				return err
			}
			if authorization := updaterRedirectAuthorization(next, via); authorization == "" || next.Header.Get("Authorization") != authorization {
				next.Header.Del("Authorization")
			}
		}
		return nil
	}
	response, err := secured.Do(req)
	if err != nil {
		return response, safeUpdaterRequestError(err)
	}
	return response, nil
}

// net/http includes raw Location values in redirect errors, including passwords
// and signed queries; malformed Locations can also appear in nested messages.
// Keep those values out of ordinary diagnostics while preserving the structured
// cause for errors.Is/As. Do not attempt brittle string-based URL redaction.
type updaterRequestError struct {
	cause   error
	message string
}

func (err *updaterRequestError) Error() string { return err.message }
func (err *updaterRequestError) Unwrap() error { return err.cause }

func safeUpdaterRequestError(err error) error {
	message := "updater HTTP request failed"
	var networkError net.Error
	switch {
	case errors.Is(err, errUpdaterRedirectAuthority):
		message = errUpdaterRedirectAuthority.Error()
	case errors.Is(err, errUpdaterRedirectLimit):
		message = errUpdaterRedirectLimit.Error()
	case errors.Is(err, context.Canceled):
		message = "updater HTTP request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		message = "updater HTTP request timed out"
	case errors.As(err, &networkError) && networkError.Timeout():
		message = "updater HTTP request timed out"
	}
	return &updaterRequestError{cause: err, message: message}
}
