# Build GoChain in a stock Go builder container
FROM golang:1.24.1-alpine AS builder

RUN apk --no-cache add build-base git gcc linux-headers
ENV D=/gochain
WORKDIR $D
# cache dependencies
ADD go.mod $D
ADD go.sum $D
RUN go mod download
# build
ENV GOFLAGS=-buildvcs=false
ADD . $D
RUN cd $D && make all && mkdir -p /tmp/gochain && cp $D/bin/* /tmp/gochain/ && cp $D/contracts/*abi /tmp/gochain/

# Pull all binaries into a second stage deploy alpine container
FROM alpine:latest

RUN apk add --no-cache ca-certificates
COPY --from=builder /tmp/gochain/* /usr/local/bin/
EXPOSE 6060 8545 8546 30303 30303/udp 30304/udp
WORKDIR /usr/local/bin
CMD [ "gochain" ]
