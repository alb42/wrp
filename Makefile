REPO ?= alb42/amifoxserver
TAG ?= latest
NAME ?= amifoxserver

all: wrp

wrp: wrp.go
	go build wrp.go

cross:
	GOOS=linux GOARCH=amd64 go build -a -o wrp-amd64-linux wrp.go
	GOOS=freebsd GOARCH=amd64 go build -a -o wrp-amd64-freebsd wrp.go
	GOOS=openbsd GOARCH=amd64 go build -a -o wrp-amd64-openbsd wrp.go
	GOOS=darwin GOARCH=amd64 go build -a -o wrp-amd64-macos wrp.go
	GOOS=darwin GOARCH=arm64 go build -a -o wrp-arm64-macos wrp.go
	GOOS=windows GOARCH=amd64 go build -a -o wrp-amd64-windows.exe wrp.go
	GOOS=linux GOARCH=arm go build -a -o wrp-arm-linux wrp.go
	GOOS=linux GOARCH=arm64 go build -a -o wrp-arm64-linux wrp.go

build: wrp
	docker build -t $(REPO):$(TAG) .

buildnc:
	docker build --no-cache -t $(REPO):$(TAG) .

run:
	docker run --rm -d -p 8080:8080 --name $(NAME) $(REPO):$(TAG)

stop:
	docker kill $(NAME)
	docker rm $(NAME)

push:
	docker push $(REPO):$(TAG)

pull:
	docker pull $(REPO):$(TAG)

logs:
	docker logs $(NAME)

clean:
	rm -rf wrp-* wrp

dockerclean:
	-docker rmi -f $(REPO)
