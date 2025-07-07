FROM golang:1.24.4 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Build the Go application with CGO disabled for static linking
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o doc2paperless -ldflags "-X 'main.version=0.1.0'"


FROM alpine:3.22.0

RUN apk add --no-cache tzdata
ENV TZ=Europe/Berlin
RUN ln -sf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone
WORKDIR /app
COPY --from=builder /app/doc2paperless .
RUN ls -l /app
RUN mkdir -p /consumefolder
EXPOSE 2112
CMD ["/app/doc2paperless"]