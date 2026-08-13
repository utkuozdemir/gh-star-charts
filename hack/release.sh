#!/usr/bin/env bash
# Cuts a release: cross-builds the binaries, writes checksums covering exactly
# this version's assets, tags, and publishes the GitHub release. The inlined
# checksum in every instance workflow depends on this file's output, so the
# asset naming and the checksums format are contracts.
set -euo pipefail

version="${1:?usage: hack/release.sh vX.Y.Z}"

case "${version}" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*)
	echo "version must look like vX.Y.Z" >&2
	exit 1
	;;
esac

cd "$(git rev-parse --show-toplevel)"

if [[ -n "$(git status --porcelain)" ]]; then
	echo "working tree is dirty; commit or stash first" >&2
	exit 1
fi

go test ./...

out="dist/${version}"
rm -rf "${out}"
mkdir -p "${out}"

for platform in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64; do
	goos="${platform%-*}"
	goarch="${platform#*-}"
	name="gh-star-charts_${version}_${platform}"

	if [[ "${goos}" == "windows" ]]; then
		name+=".exe"
	fi

	GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w -X main.version=${version}" -o "${out}/${name}" .
done

(cd "${out}" && shasum -a 256 gh-star-charts_* >checksums.txt)

git tag "${version}"
git push origin "${version}"

gh release create "${version}" "${out}"/gh-star-charts_* "${out}/checksums.txt" \
	--title "${version}" --generate-notes
