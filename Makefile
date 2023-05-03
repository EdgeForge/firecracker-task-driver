.PHONY: build shrink image all

EXEC=firecracker-task-driver
TAG=$(shell git tag --points-at HEAD)
TAG ?= x.x.x

.phony: build shrink bin image all race

build:
	CGO_ENABLED=0 go build -ldflags="-X main.Version=${TAG} -extldflags=-static -s -w" -o ${EXEC}

race:
	CC=musl-gcc CGO_ENABLED=1 go build -race -ldflags='-linkmode external -extldflags "-static -s -w"' -o ${EXEC}

shrink:
	upx ${EXEC}

bin: build shrink

all: build shrink image
