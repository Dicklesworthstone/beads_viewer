package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const updaterHTTPFakeToken = "bounded-fake-http-credential-sentinel"

func authorizeUpdaterHTTPTest(t *testing.T) {
	t.Helper()
	isolateUpdaterConfig(t)
	t.Setenv(EnvUseToken, "1")
	t.Setenv("GITHUB_TOKEN", updaterHTTPFakeToken)
}

func useUpdaterTestTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original })
}

func updaterHTTPResponse(req *http.Request, status int, location, body string) *http.Response {
	header := make(http.Header)
	if location != "" {
		header.Set("Location", location)
	}
	return &http.Response{
		StatusCode: status, Header: header, Request: req,
		Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)),
	}
}

// Exercise every request site through the HTTP client's real redirect loop. The
// transport only returns in-memory responses; no test URL can reach a network.
func TestUpdaterRedirectPolicyAtEveryRequestSite(t *testing.T) {
	authorizeUpdaterHTTPTest(t)
	const metadata = `{"tag_name":"v0.0.1","html_url":"https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.0.1","assets":[]}`
	const payload = "bounded-download-fixture"
	checksum := sha256.Sum256([]byte(payload))
	for _, operation := range []string{"startup check", "latest release", "checksum bytes", "archive download"} {
		t.Run(operation, func(t *testing.T) {
			host := "api.github.com"
			body := metadata
			if operation == "checksum bytes" || operation == "archive download" {
				host, body = "github.com", payload
			}
			for _, destination := range []struct {
				name     string
				location string
				wantAuth bool
				wantErr  bool
			}{
				{"relative same origin", "/next?download=1", true, false},
				{"DNS case and default port", "https://" + strings.ToUpper(host) + ":443/next", true, false},
				{"release CDN", "https://release-assets.githubusercontent.com/file?signature=fake", false, false},
				{"other HTTPS CDN", "https://cdn.example.invalid/file?signature=fake", false, false},
				{"subdomain receives no bearer", "https://child." + host + "/next", false, false},
				{"Unicode case fold receives no bearer", "https://gİthub.com/next", false, false},
				{"HTTP downgrade", "http://" + host + "/next", false, true},
				{"HTTP downgrade with port", "http://" + host + ":8080/next", false, true},
				{"nonstandard HTTPS port", "https://" + host + ":444/next", false, true},
				{"empty port", "https://" + host + ":/next", false, true},
				{"URL credentials", "https://fixture:fake@" + host + "/next", false, true},
				{"opaque URL", "https:" + host + "/next", false, true},
				{"missing authority", "https:///next", false, true},
				{"non-HTTP scheme", "file:///bounded-fixture", false, true},
			} {
				t.Run(destination.name, func(t *testing.T) {
					var auth []bool
					useUpdaterTestTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
						auth = append(auth, req.Header.Get("Authorization") == "Bearer "+updaterHTTPFakeToken)
						if len(auth) == 1 {
							return updaterHTTPResponse(req, http.StatusFound, destination.location, ""), nil
						}
						if len(auth) > 2 {
							return nil, errors.New("bounded fixture received an extra request")
						}
						return updaterHTTPResponse(req, http.StatusOK, "", body), nil
					}))
					var err error
					switch operation {
					case "startup check":
						_, _, err = CheckForUpdates()
					case "latest release":
						_, err = GetLatestRelease()
					case "checksum bytes":
						asset := &Asset{
							Name: "checksums.txt", BrowserDownloadURL: "https://github.com/fixture/checksums.txt",
							Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(checksum[:]),
						}
						_, err = downloadReleaseAssetBytes(context.Background(), &http.Client{Timeout: time.Second}, asset, 1024)
					case "archive download":
						dest := filepath.Join(t.TempDir(), "archive")
						err = downloadFile("https://github.com/fixture/archive", dest, int64(len(payload)))
						if err == nil {
							data, readErr := os.ReadFile(dest)
							if readErr != nil || string(data) != payload {
								t.Fatalf("safe redirected download was not preserved: %v", readErr)
							}
						} else if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
							t.Errorf("rejected redirect created a download: %v", statErr)
						}
					}
					if (err != nil) != destination.wantErr {
						t.Fatalf("redirect result error = %v, want failure %v", err, destination.wantErr)
					}
					wantAuth := []bool{true, destination.wantAuth}
					if destination.wantErr {
						wantAuth = []bool{true}
					}
					if !reflect.DeepEqual(auth, wantAuth) {
						t.Errorf("outgoing fake-authorization presence = %v, want %v", auth, wantAuth)
					}
				})
			}
		})
	}
}

func TestUpdaterRedirectNeverRestoresCrossOriginAuthorization(t *testing.T) {
	authorizeUpdaterHTTPTest(t)
	for _, firstHop := range []string{
		"https://child.api.github.com/first",
		"https://github.com/first",
		"https://release-assets.githubusercontent.com/first",
	} {
		t.Run(firstHop, func(t *testing.T) {
			var auth []bool
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				auth = append(auth, req.Header.Get("Authorization") == "Bearer "+updaterHTTPFakeToken)
				switch len(auth) {
				case 1:
					return updaterHTTPResponse(req, http.StatusFound, firstHop, ""), nil
				case 2:
					return updaterHTTPResponse(req, http.StatusFound, "https://API.GITHUB.COM:443/return", ""), nil
				case 3:
					return updaterHTTPResponse(req, http.StatusOK, "", "bounded fixture"), nil
				default:
					return nil, errors.New("bounded redirect fixture exceeded request limit")
				}
			})}
			req, err := http.NewRequest(http.MethodGet, baseURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			setGitHubAuth(req)
			resp, err := doUpdaterRequest(client, req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if !reflect.DeepEqual(auth, []bool{true, false, false}) {
				t.Errorf("cross-origin return restored fake bearer: %v", auth)
			}
		})
	}
}

func TestUpdaterRedirectRechecksConsentWithoutGrantingNewTokens(t *testing.T) {
	for _, change := range []string{"revoke consent", "remove token", "replace token", "grant after initial request"} {
		t.Run(change, func(t *testing.T) {
			authorizeUpdaterHTTPTest(t)
			initialAuth := true
			if change == "grant after initial request" {
				t.Setenv(EnvUseToken, "0")
				initialAuth = false
			}
			var auth []bool
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				auth = append(auth, req.Header.Get("Authorization") != "")
				if len(auth) == 1 {
					switch change {
					case "revoke consent":
						t.Setenv(EnvUseToken, "0")
					case "remove token":
						t.Setenv("GITHUB_TOKEN", "")
					case "replace token":
						t.Setenv("GITHUB_TOKEN", "bounded-new-fake-token")
					case "grant after initial request":
						t.Setenv(EnvUseToken, "1")
					}
					return updaterHTTPResponse(req, http.StatusFound, "/same-origin", ""), nil
				}
				if len(auth) > 2 {
					return nil, errors.New("bounded consent fixture received an extra request")
				}
				return updaterHTTPResponse(req, http.StatusOK, "", "bounded fixture"), nil
			})}
			req, err := http.NewRequest(http.MethodGet, baseURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			setGitHubAuth(req)
			resp, err := doUpdaterRequest(client, req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if !reflect.DeepEqual(auth, []bool{initialAuth, false}) {
				t.Errorf("redirect retained or introduced stale authority: %v", auth)
			}
		})
	}
}

func TestUpdaterRedirectLimit(t *testing.T) {
	authorizeUpdaterHTTPTest(t)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests > 11 {
			return nil, errors.New("bounded redirect-limit fixture stopped")
		}
		return updaterHTTPResponse(req, http.StatusFound, "/loop", ""), nil
	})}
	req, err := http.NewRequest(http.MethodGet, baseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	setGitHubAuth(req)
	resp, err := doUpdaterRequest(client, req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "too many redirects") || requests != 10 {
		t.Fatalf("redirect limit: requests=%d, err=%v", requests, err)
	}
}

func updaterRedirectReleaseFixture() (*Release, string) {
	manifest := strings.TrimPrefix(testSHA256DigestA, "sha256:") + "  " + stableAssetName() + "\n"
	digest := sha256.Sum256([]byte(manifest))
	prefix := "https://github.com/Dicklesworthstone/beads_viewer/releases/download/v99.0.0/"
	return &Release{
		TagName: "v99.0.0", HTMLURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v99.0.0",
		Assets: []Asset{
			{Name: stableAssetName(), BrowserDownloadURL: prefix + stableAssetName(), Size: 1, State: "uploaded", Digest: testSHA256DigestA},
			{Name: "checksums.txt", BrowserDownloadURL: prefix + "checksums.txt", Size: int64(len(manifest)), State: "uploaded", Digest: "sha256:" + hex.EncodeToString(digest[:])},
		},
	}, manifest
}

func TestUpdaterCheckRejectsInsecureChecksumRedirect(t *testing.T) {
	authorizeUpdaterHTTPTest(t)
	release, _ := updaterRedirectReleaseFixture()
	metadata, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	useUpdaterTestTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			return updaterHTTPResponse(req, http.StatusOK, "", string(metadata)), nil
		case 2:
			return updaterHTTPResponse(req, http.StatusFound, "http://github.com:8080/checksums", ""), nil
		default:
			return nil, errors.New("insecure checksum redirect reached transport")
		}
	}))
	_, _, err = CheckForUpdates()
	if err == nil || requests != 2 {
		t.Fatalf("checksum redirect policy was bypassed: requests=%d, err=%v", requests, err)
	}
}

func TestUpdaterInitialReleaseAuthorityRejectsUnicodeCaseFolds(t *testing.T) {
	authorizeUpdaterHTTPTest(t)
	for _, field := range []string{"page", "archive", "checksums"} {
		for _, authority := range []string{"gİthub.com", "github.com:444", "fixture:fake@github.com", "child.github.com"} {
			t.Run(field+"/"+authority, func(t *testing.T) {
				release, _ := updaterRedirectReleaseFixture()
				var target *string
				switch field {
				case "page":
					target = &release.HTMLURL
				case "archive":
					target = &release.Assets[0].BrowserDownloadURL
				case "checksums":
					target = &release.Assets[1].BrowserDownloadURL
				}
				*target = strings.Replace(*target, "github.com", authority, 1)
				requests := 0
				useUpdaterTestTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
					requests++
					return nil, errors.New("invalid release authority reached transport")
				}))
				if err := ValidateReleaseForUpdate(release); err == nil {
					t.Fatal("invalid initial release authority passed validation")
				}
				if _, err := PerformUpdate(release, io.Discard); err == nil || requests != 0 {
					t.Fatalf("invalid initial URL reached updater networking: requests=%d, err=%v", requests, err)
				}
			})
		}
	}
}

func TestUpdaterHTTPClientPreservesStricterPolicyConcurrently(t *testing.T) {
	authorizeUpdaterHTTPTest(t)
	refusal := errors.New("test caller refused redirect")
	var callbacks atomic.Int32
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return updaterHTTPResponse(req, http.StatusFound, "/next", ""), nil
		}),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			callbacks.Add(1)
			return refusal
		},
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			req, err := http.NewRequest(http.MethodGet, baseURL, nil)
			if err != nil {
				results <- err
				return
			}
			setGitHubAuth(req)
			resp, err := doUpdaterRequest(client, req)
			if resp != nil {
				resp.Body.Close()
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; !errors.Is(err, refusal) {
			t.Errorf("caller redirect refusal was lost: %v", err)
		}
	}
	if callbacks.Load() != 2 {
		t.Errorf("original client callback was mutated or bypassed: calls=%d", callbacks.Load())
	}
}

func TestUpdaterRedirectRevalidatesCallbackMutations(t *testing.T) {
	authorizeUpdaterHTTPTest(t)
	for _, mutation := range []string{"strip bearer", "change origin", "HTTP downgrade", "malformed authority"} {
		t.Run(mutation, func(t *testing.T) {
			requests := 0
			client := &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					requests++
					if requests == 1 {
						return updaterHTTPResponse(req, http.StatusFound, "/next", ""), nil
					}
					if req.Header.Get("Authorization") != "" {
						t.Error("callback widened redirect authorization")
					}
					return updaterHTTPResponse(req, http.StatusOK, "", "bounded fixture"), nil
				}),
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					switch mutation {
					case "strip bearer":
						req.Header.Del("Authorization")
					case "change origin":
						req.URL.Host = "other.example.invalid"
					case "HTTP downgrade":
						req.URL.Scheme = "http"
					case "malformed authority":
						req.URL.Host = "github.com:invalid"
					}
					return nil
				},
			}
			req, err := http.NewRequest(http.MethodGet, baseURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			setGitHubAuth(req)
			resp, err := doUpdaterRequest(client, req)
			if resp != nil {
				resp.Body.Close()
			}
			wantErr := mutation == "HTTP downgrade" || mutation == "malformed authority"
			if (err != nil) != wantErr {
				t.Errorf("mutated redirect error = %v, want failure %v", err, wantErr)
			}
			wantRequests := 2
			if wantErr {
				wantRequests = 1
			}
			if requests != wantRequests {
				t.Errorf("mutated redirect reached transport %d times, want %d", requests, wantRequests)
			}
		})
	}
}

func TestUpdaterRedirectErrorsDoNotDiscloseURLCredentials(t *testing.T) {
	authorizeUpdaterHTTPTest(t)
	for _, location := range []string{
		"https://fixture:FAKE_URL_PASSWORD@github.com/file?signature=FAKE_SIGNED_QUERY",
		"http://github.com/file?signature=FAKE_SIGNED_QUERY",
		"https://fixture:FAKE_URL_PASSWORD@github.com:bad/file?signature=FAKE_SIGNED_QUERY",
	} {
		t.Run(location, func(t *testing.T) {
			requests := 0
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				if requests > 1 {
					return nil, errors.New("rejected fixture reached transport")
				}
				return updaterHTTPResponse(req, http.StatusFound, location, ""), nil
			})}
			req, err := http.NewRequest(http.MethodGet, baseURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			setGitHubAuth(req)
			resp, err := doUpdaterRequest(client, req)
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil || requests != 1 {
				t.Fatalf("invalid redirect was not rejected before transport: requests=%d, err=%v", requests, err)
			}
			for _, marker := range []string{"FAKE_URL_PASSWORD", "FAKE_SIGNED_QUERY", updaterHTTPFakeToken} {
				if strings.Contains(err.Error(), marker) {
					t.Error("redirect error disclosed a fake URL credential")
				}
			}
			var original *url.Error
			if !errors.As(err, &original) {
				t.Error("safe error lost structured underlying HTTP error")
			}
		})
	}
}
