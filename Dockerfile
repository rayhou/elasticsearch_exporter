FROM golang:alpine

RUN apk update && apk add --update alpine-sdk

ADD .   /go/src/github.com/justwatchcom/elasticsearch_exporter

RUN \
    cd /go/src/github.com/justwatchcom/elasticsearch_exporter && \
    go build && \
    go install

EXPOSE      9108
ENTRYPOINT  [ "elasticsearch_exporter" ]
