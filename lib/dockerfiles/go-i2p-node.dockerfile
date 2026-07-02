FROM golang:1.26-alpine

WORKDIR /go/src/app

# Install required build tools and dependencies
RUN apk update && apk add --no-cache \
    git \
    make \
    build-base \
    gcc \
    musl-dev

RUN git clone https://github.com/go-i2p/go-i2p.git

WORKDIR /go/src/app/go-i2p/

RUN go mod tidy

ENV DEBUG_I2P=debug

#RUN go build -o /usr/local/bin/go-i2p-router
RUN make build
RUN mv go-i2p /usr/local/bin/
# Expose the default router port (adjust as needed)
EXPOSE 7654

CMD ["go-i2p"]
