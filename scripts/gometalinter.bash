#!/bin/bash
# Copyright 2013 Canonical Ltd.
# Licensed under the AGPLv3, see LICENCE file for details.
exitstatus=0

OUTPUT_FILE=$(mktemp) || { echo "Failed to create temp file"; exit 1; }
MOCK_HEADER="// Code generated by MockGen. DO NOT EDIT."

go get -u github.com/alecthomas/gometalinter

gometalinter --install > /dev/null
gometalinter --disable-all \
    --enable=goimports \
    --enable=misspell \
    --enable=unconvert \
    --enable=vet \
    --enable=vetshadow \
    --deadline=240s \
    ./... &> $OUTPUT_FILE

# go through each gometalinter error and check to see if it's related 
# to a mock file.
invalidLines=`cat $OUTPUT_FILE | grep -v '\(.*\/[^\/]*\.go\):[[:digit:]]*:[[:digit:]]*:.*' | wc -l`
if [ $invalidLines -ne 0 ]; then
    # there was a parse error when reading the gometalinter output
    exitstatus=1
    cat $OUTPUT_FILE
else
    # valid set of lines, go through each and make sure they're valid
    # mock files, if not exit out.
    lines=`wc -l $OUTPUT_FILE | awk '{print $1}'`
    if [ $lines -ne 0 ]; then
        while read p; do
            header=`echo $p | cut -d':' -f1 | xargs head -n1`
            if [ "$header" != "$MOCK_HEADER" ]; then
                exitstatus=1
                (>&2 echo $p)
            fi
        done <$OUTPUT_FILE
    fi
fi
rm $OUTPUT_FILE
exit $exitstatus
