# Verifying a release

Pushing a release tag `vX.Y.Z` starts the `attest` job in `.gitlab-ci.yml`. It
writes a provenance document, `provenance.json`, signs it with
[Sigstore](https://www.sigstore.dev/) under the identity GitLab gives that
pipeline, and stores the document and its signature bundle,
`provenance.json.sigstore.json`, in this project's package registry. The GitLab
release of the tag links both files. No key is involved: the signing
certificate is issued for that one job and names it, and the signature is
recorded in Sigstore's public transparency log.

Releases tagged before this job existed have no document, and neither has
v1.61.0, the first tag the job ran for: that run copied `vendor.json` into the
document from a working tree `make vendor-check` had just stamped again with
the job's own Go release, and `attest -verify` refused the document against the
committed file. Since v1.61.1 the copy is taken from the tagged commit.

## What a verified document tells you

- It was signed by a pipeline of `gitlab.com/cznic/sqlite` (project ID
  9241019), started by the push of that tag, on a GitLab-hosted runner, running
  the `.gitlab-ci.yml` of the commit the document names.
- Before signing, that pipeline checked that the tag named the commit and that
  `vendor.json` agreed with `go.mod` and with the committed `lib/` and `vec/`
  (`internal/vendorstamp`), and it cloned the `libsqlite3` and `libsqlite_vec`
  commits `vendor.json` names and vendored again from them, without the
  cross-builds (`make vendor-check`). Every file `vendor.json` covers
  (`lib/sqlite*.go`, `vec/vec*.go`, `LICENSE-SQLITE_VEC`) came back byte for
  byte, with no file missing or extra, and so did `vendor.json` itself apart
  from the Go release it records: `vendor` in the document is the maintainer's
  stamp, and `builder.go` the Go that reproduced it. It then downloaded the
  module twice, once from
  gitlab.com and once from proxy.golang.org checked against sum.golang.org,
  each into an empty cache, and got the same hashes from both. `observed` in
  the document gives the time window of those checks by the runner's clock; the
  time of the signature is in the bundle.
- `vendor`, a copy of the release's `vendor.json`, names the
  `modernc.org/libsqlite3` and `modernc.org/libsqlite_vec` commits and the Go
  toolchain `lib/` and `vec/` were vendored with.

## What it does not tell you

- That the code is safe, or that the builders were green when the tag was
  pushed. Tags are pushed by hand; see HACKING.md.
- Who pushed the tag. Only maintainers can create tags in this project, and
  whoever holds a maintainer account can push one and get it attested.
- That the Go in `libsqlite3` and `libsqlite_vec` at those commits is a
  faithful transpilation of the C it comes from. The job reproduces the copy
  of that Go into `lib/` and `vec/`, not the transpilation behind it.
- More integrity than you already have. sum.golang.org already guarantees that
  everyone gets the same bytes for a version. The document adds where those
  bytes came from.
- Anything beyond the commit itself. The tagged commit's own code runs in the
  job that holds the signing identity, so a commit that changes
  `.gitlab-ci.yml` or `attestgen/` can have the job sign whatever it likes. A
  document is as trustworthy as the commit it names.

## Verify with attestgen

You need Go and [cosign](https://github.com/sigstore/cosign) v3.0.4 or later,
or v2.6.2 or later. Older versions are affected by
[GHSA-whqx-f9j3-ch6m](https://github.com/sigstore/cosign/security/advisories/GHSA-whqx-f9j3-ch6m).

```
V=vX.Y.Z   # a release whose GitLab release links a provenance document
P=https://gitlab.com/api/v4/projects/cznic%2Fsqlite/packages/generic/attestation/$V
curl -fsSLO $P/provenance.json
curl -fsSLO $P/provenance.json.sigstore.json
(cd attestgen && go build -tags none -o ../attest .)   # in a checkout of this repository
./attest -verify -tag $V provenance.json provenance.json.sigstore.json
```

It runs `cosign verify-blob`, then checks the claims in the certificate listed
below, then downloads the module through proxy.golang.org and compares its
hashes and its `vendor.json` with the document. Exit status 0 means every check
passed.

## Verify by hand

```
cosign verify-blob provenance.json \
  --bundle provenance.json.sigstore.json \
  --certificate-identity "https://gitlab.com/cznic/sqlite//.gitlab-ci.yml@refs/tags/$V" \
  --certificate-oidc-issuer https://gitlab.com \
  --use-signed-timestamps
```

This checks the signature, the transparency log entry, the timestamp, and that
the certificate names this project's pipeline for the tag. cosign has no flags
for the rest of what GitLab puts in the certificate, so read it yourself:

```
jq -r .verificationMaterial.certificate.rawBytes provenance.json.sigstore.json |
  base64 -d | openssl x509 -inform der -noout -text
```

| Extension | Must be |
| --- | --- |
| `1.3.6.1.4.1.57264.1.13` | the `commit` in the document |
| `1.3.6.1.4.1.57264.1.10` | the same commit |
| `1.3.6.1.4.1.57264.1.14` | `refs/tags/` followed by the tag |
| `1.3.6.1.4.1.57264.1.15` | `9241019` |
| `1.3.6.1.4.1.57264.1.20` | `push` |
| `1.3.6.1.4.1.57264.1.11` | `gitlab-hosted` |

openssl prints each value after one or two characters of encoding.

Then compare the document with the module. Download it into an empty module
of its own, with a fresh cache and none of your Go settings, so that neither a
`go.sum` nor a `GONOSUMDB` or `GOFLAGS` of yours answers in place of
sum.golang.org:

```
D=$(mktemp -d) && cd "$D" && printf 'module scratch\n\ngo 1.25.0\n' > go.mod
env -i PATH="$PATH" HOME="$D" GOENV=off GOFLAGS=-modcacherw GOWORK=off \
  GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org \
  GOMODCACHE="$D/cache" GOPATH="$D/gopath" GOCACHE="$D/gocache" \
  go mod download -json modernc.org/sqlite@$V
```

In the document, `schema` must be `modernc.org/sqlite/attestation/v1`,
`module` `modernc.org/sqlite`, `version` the tag, and `commit` the commit in the
certificate. Against the download: `Sum` must equal `zip_h1`, `GoModSum` must
equal `gomod_h1`, the SHA-256 of `vendor.json` in the reported `Dir` must equal
`vendor_json_sha256`, and `vendor` in the document must hold the same JSON as
that file. `attest -verify` makes all of these checks.
