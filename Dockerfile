# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/stats-lite .

# WHY: a static binary needs no libc; distroless keeps the image under 15 MB
# and ships no shell for an attacker to use.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/stats-lite /stats-lite
EXPOSE 8080
ENTRYPOINT ["/stats-lite"]
