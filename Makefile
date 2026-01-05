.PHONY: gochain android ios gochain-cross swarm evm all test clean
.PHONY: gochain-linux gochain-linux-386 gochain-linux-amd64 gochain-linux-mips64 gochain-linux-mips64le
.PHONY: gochain-linux-arm gochain-linux-arm-5 gochain-linux-arm-6 gochain-linux-arm-7 gochain-linux-arm64
.PHONY: gochain-darwin gochain-darwin-386 gochain-darwin-amd64
.PHONY: gochain-windows gochain-windows-386 gochain-windows-amd64
.PHONY: docker release

GOBIN = $(shell pwd)/build/bin
GO ?= latest

# Compare current go version to minimum required version. Exit with \
# error message if current version is older than required version.
# Set min_ver to the mininum required Go version such as "1.12"
min_ver := 1.12
ver = $(shell go version)
ver2 = $(word 3, ,$(ver))
cur_ver = $(subst go,,$(ver2))
ver_check := $(filter $(min_ver),$(firstword $(sort $(cur_ver) \
$(min_ver))))
ifeq ($(ver_check),)
$(error Running Go version $(cur_ver). Need $(min_ver) or higher. Please upgrade Go version)
endif

gochain:
	cd cmd/gochain; go build -o ../../bin/gochain
	@echo "Done building."
	@echo "Run \"bin/gochain\" to launch gochain."

build:
	go build ./...

bootnode:
	cd cmd/bootnode; go build -o ../../bin/gochain-bootnode
	@echo "Done building."
	@echo "Run \"bin/gochain-bootnode\" to launch gochain."

docker:
	docker build -t gochain/gochain .

all: bootnode gochain

release:
	./release.sh

install: all
	cp bin/gochain-bootnode $(GOPATH)/bin/gochain-bootnode
	cp bin/gochain $(GOPATH)/bin/gochain

android:
	build/env.sh go run build/ci.go aar --local
	@echo "Done building."
	@echo "Import \"$(GOBIN)/gochain.aar\" to use the library."

ios:
	build/env.sh go run build/ci.go xcode --local
	@echo "Done building."
	@echo "Import \"$(GOBIN)/gochain.framework\" to use the library."

test:
	go test ./...

clean:
	rm -fr build/_workspace/pkg/ $(GOBIN)/*

# The devtools target installs tools required for 'go generate'.
# You need to put $GOBIN (or $GOPATH/bin) in your PATH to use 'go generate'.

devtools:
	env GOBIN= go get -u golang.org/x/tools/cmd/stringer
	env GOBIN= go get -u github.com/kevinburke/go-bindata/go-bindata
	env GOBIN= go get -u github.com/fjl/gencodec
	env GOBIN= go get -u google.golang.org/protobuf/protoc-gen-go
	env GOBIN= go install ./cmd/abigen
	@type "npm" 2> /dev/null || echo 'Please install node.js and npm'
	@type "solc" 2> /dev/null || echo 'Please install solc'
	@type "protoc" 2> /dev/null || echo 'Please install protoc'

# Cross Compilation Targets (xgo)

gochain-cross: gochain-linux gochain-darwin gochain-windows gochain-android gochain-ios
	@echo "Full cross compilation done:"
	@ls -ld $(GOBIN)/gochain-*

gochain-linux: gochain-linux-386 gochain-linux-amd64 gochain-linux-arm gochain-linux-mips64 gochain-linux-mips64le
	@echo "Linux cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-*

gochain-linux-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/386 -v ./cmd/gochain
	@echo "Linux 386 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep 386

gochain-linux-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/amd64 -v ./cmd/gochain
	@echo "Linux amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep amd64

gochain-linux-arm: gochain-linux-arm-5 gochain-linux-arm-6 gochain-linux-arm-7 gochain-linux-arm64
	@echo "Linux ARM cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep arm

gochain-linux-arm-5:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-5 -v ./cmd/gochain
	@echo "Linux ARMv5 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep arm-5

gochain-linux-arm-6:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-6 -v ./cmd/gochain
	@echo "Linux ARMv6 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep arm-6

gochain-linux-arm-7:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-7 -v ./cmd/gochain
	@echo "Linux ARMv7 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep arm-7

gochain-linux-arm64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm64 -v ./cmd/gochain
	@echo "Linux ARM64 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep arm64

gochain-linux-mips:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips --ldflags '-extldflags "-static"' -v ./cmd/gochain
	@echo "Linux MIPS cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep mips

gochain-linux-mipsle:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mipsle --ldflags '-extldflags "-static"' -v ./cmd/gochain
	@echo "Linux MIPSle cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep mipsle

gochain-linux-mips64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64 --ldflags '-extldflags "-static"' -v ./cmd/gochain
	@echo "Linux MIPS64 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep mips64

gochain-linux-mips64le:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64le --ldflags '-extldflags "-static"' -v ./cmd/gochain
	@echo "Linux MIPS64le cross compilation done:"
	@ls -ld $(GOBIN)/gochain-linux-* | grep mips64le

gochain-darwin: gochain-darwin-386 gochain-darwin-amd64
	@echo "Darwin cross compilation done:"
	@ls -ld $(GOBIN)/gochain-darwin-*

gochain-darwin-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/386 -v ./cmd/gochain
	@echo "Darwin 386 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-darwin-* | grep 386

gochain-darwin-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/amd64 -v ./cmd/gochain
	@echo "Darwin amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-darwin-* | grep amd64

gochain-windows: gochain-windows-386 gochain-windows-amd64
	@echo "Windows cross compilation done:"
	@ls -ld $(GOBIN)/gochain-windows-*

gochain-windows-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/386 -v ./cmd/gochain
	@echo "Windows 386 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-windows-* | grep 386

gochain-windows-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/amd64 -v ./cmd/gochain
	@echo "Windows amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gochain-windows-* | grep amd64
