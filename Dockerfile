FROM golang:1.7.4-alpine

COPY . /go/src/github.com/linki/deputy
RUN go install -v github.com/linki/deputy

ENTRYPOINT ["/go/bin/deputy"]
