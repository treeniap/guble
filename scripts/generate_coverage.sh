#!/bin/bash -e
# Run from directory above via ./scripts/cov.sh
# Require installation of: `github.com/wadey/gocovmerge`

rm -rf ./cov
mkdir cov

for dir in $(find . -maxdepth 10 -not -path './.git*' -not -path '*/_*' -type d);
do
if ls $dir/*.go &> /dev/null; then
    go test -v -covermode=atomic -coverprofile=./cov/$dir.out ./$dir
fi
done

gocovmerge ./cov/*.out > full_cov.out
rm -rf ./cov
