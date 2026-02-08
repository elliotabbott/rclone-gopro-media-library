#!/bin/sh

# Cache volume that for caching across builds
sudo docker volume create go-cache > /dev/null

sudo docker run \
	--rm \
	-v "$PWD:$PWD" \
	-v go-cache:/go \
	-e "GOCACHE=/go/.cache" \
	-w "$PWD" \
	golang:1.24-alpine \
	sh -c "go build -ldflags='-s -w' rclone.go && chown $(id -u):$(id -g) rclone"
