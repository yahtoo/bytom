FROM alpine:3.5

ADD . /go/src/github.com/bytom
ENV GOPATH /go
RUN \
	apk add --update git go make gcc musl-dev linux-headers glide	&& \
	(cd go/src/github.com/bytom && make install)					&& \
	(cd go/src/github.com/bytom/cmd/bytomd && go build)				&& \
	(cd go/src/github.com/bytom/cmd/bytomcli && go build)			&& \
	cp go/src/github.com/bytom/cmd/bytomd /usr/local/bin/			&& \
	cp go/src/github.com/bytom/cmd/bytomcli /usr/local/bin/			&& \
	apk del git go make gcc musl-dev linux-headers					&& \
	rm -rf /go && rm -rf /var/cache/apk/*

EXPOSE 1999 46656 46657
CMD ["bytom"]

