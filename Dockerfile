FROM golang:1.7.4-alpine

COPY . /go/src/github.com/linki/snapshot-controller
RUN go install -v github.com/linki/snapshot-controller

ENTRYPOINT ["/go/bin/snapshot-controller"]
