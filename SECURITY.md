# Security policy

## Reporting a vulnerability

Report suspected vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/JohanLindvall/bufpool/security/advisories/new),
not in a public issue. The report is visible only to you and the maintainer
while a fix is prepared.

Please include the bufpool and Go versions you used, and a minimal program or
test that reproduces the problem.

## Supported versions

Only the latest release is supported: fixes ship as a new patch version, and
earlier versions are not patched. Every green commit on `main` is tagged as the
next patch release, so upgrading picks up a fix as soon as it is merged:

```sh
go get github.com/JohanLindvall/bufpool@latest
```

## Scope

bufpool is pure Go and uses neither `unsafe` nor cgo. In scope are, for
example:

- two live buffers sharing a backing array, so that one can read or overwrite
  the other's contents;
- memory retained beyond what the [strike heuristic](README.md#the-strike-heuristic)
  and the package documentation describe;
- a panic other than the documented ones: `ErrTooLarge` when a buffer cannot
  grow, a negative count, or an `io.Reader` or `io.Writer` that violates its
  contract.

Behavior the documentation already describes is not a vulnerability. In
particular, a released buffer's bytes stay in the pool and are visible to the
next caller that gets its array unless `Wipe` was called (see
[Secrets](README.md#secrets)), and slices returned by `Bytes`, `Next`,
`ReadAllBytes` or `Scratch` must not be used after `Release`, `Close` or
`Reset`.
