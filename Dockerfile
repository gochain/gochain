# Build Geth in a stock Go builder container
FROM golang:1.9-alpine as builder

RUN apk add --no-cache make gcc musl-dev linux-headers

ADD . /gochain
RUN cd /gochain && make all

# Pull all binaries into a second stage deploy alpine container
FROM alpine:latest

RUN apk add --no-cache ca-certificates
COPY --from=builder /gochain/build/bin/* /usr/local/bin/

EXPOSE 8545 8546 30303 30303/udp 30304/udp
