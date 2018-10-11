# Build GoChain in a stock Go builder container
FROM golang:1.11-alpine as builder

RUN apk --no-cache add build-base git bzr mercurial gcc linux-headers
ENV D=/go/src/github.com/gochain-io/gochain
RUN go get -u github.com/golang/dep/cmd/dep
ADD Gopkg.* $D/
RUN cd $D && dep ensure --vendor-only
ADD . $D
RUN cd $D && make all && mkdir -p /tmp/gochain && cp $D/bin/* /tmp/gochain/

# Pull all binaries into a second stage deploy alpine container
FROM alpine:latest

RUN apk add --no-cache ca-certificates
COPY --from=builder /tmp/gochain/* /usr/local/bin/
EXPOSE 6060 8545 8546 30303 30303/udp 30304/udp
CMD [ "gochain" ]
