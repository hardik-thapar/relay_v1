#!/bin/sh
# Download the three papers at exactly the versions the brief names.
set -eu
dir=data/pdf
mkdir -p "$dir"
for id in 1706.03762v7 2410.00037v1 2507.07955v2; do
	[ -s "$dir/$id.pdf" ] || curl -sSfL -o "$dir/$id.pdf" "https://arxiv.org/pdf/$id"
	echo "$dir/$id.pdf $(wc -c < "$dir/$id.pdf") bytes"
done
