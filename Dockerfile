FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/robot-site .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 app
USER app
WORKDIR /app
COPY --from=build /out/robot-site /usr/local/bin/robot-site
EXPOSE 8080
ENTRYPOINT ["robot-site"]
